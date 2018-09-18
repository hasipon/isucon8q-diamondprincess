package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/gorilla/sessions"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/middleware"
)

func must(err error) {
	if err != nil {
		log.Println(err)
		panic(err)
	}
}

// =======================================
// ON MEMORY CACHE
// =======================================
var (
	reqMtx          = sync.Mutex{}
	cMtx            = sync.Mutex{}
	cUser           = map[int64]*User{} // UserID => User
	cUserNameToID   = map[string]int64{}
	cSheet          = map[int64]Sheet{}
	cSheetSlice     = []Sheet{}
	cEventSheet     = map[int64]map[int64]*Sheet{}
	cReservation    = map[int64]*Reservation{}
	cReservedTimes  = map[int64]map[int64]int64{}
	cReservedUserID = map[int64]map[int64]int64{}
	cEvent          = map[int64]*Event{}
)

func InitCache() {
	cUser = map[int64]*User{}
	cUserNameToID = map[string]int64{}
	cSheet = map[int64]Sheet{}
	cSheetSlice = []Sheet{}
	cEventSheet = map[int64]map[int64]*Sheet{}
	cReservedTimes = map[int64]map[int64]int64{}
	cReservedUserID = map[int64]map[int64]int64{}
	cReservation = map[int64]*Reservation{}

	InitUser()
	InitSheet()
	InitReservation()
	InitEvent()
	InitReservedTime()
}

// User
func GetUserByName(userName string) *User {
	cMtx.Lock()
	defer cMtx.Unlock()
	id, ok := cUserNameToID[userName]
	if !ok {
		return nil
	}
	return cUser[id]
}

func GetUser(userID int64) *User {
	cMtx.Lock()
	defer cMtx.Unlock()
	return cUser[userID]
}

func AddUser(user *User) {
	cMtx.Lock()
	defer cMtx.Unlock()
	if user == nil {
		panic("nil user")
	}
	cUserNameToID[user.LoginName] = user.ID
	cUser[user.ID] = user
}

func GetReservation(id int64) *Reservation {
	cMtx.Lock()
	defer cMtx.Unlock()
	return cReservation[id]
}

func AddReservation(r *Reservation) {
	cMtx.Lock()
	defer cMtx.Unlock()
	cReservation[r.ID] = r

	if _, ok := cReservedTimes[r.EventID]; !ok {
		cReservedTimes[r.EventID] = map[int64]int64{}
	}
	if _, ok := cReservedUserID[r.EventID]; !ok {
		cReservedUserID[r.EventID] = map[int64]int64{}
	}
	cReservedTimes[r.EventID][r.SheetID] = r.ReservedAt.Unix()
	cReservedUserID[r.EventID][r.SheetID] = r.UserID
}

func CancelReservation(r *Reservation, t time.Time) {
	if r == nil {
		panic("r is nil")
	}
	cMtx.Lock()
	defer cMtx.Unlock()
	r.CanceledAt = &t
	r.CanceledAtUnix = t.Unix()

	delete(cReservedTimes[r.EventID], r.SheetID)
	delete(cReservedUserID[r.EventID], r.SheetID)
}

func GetReservedTimes(eventID int64, sheetID int64) (int64, bool) {
	cMtx.Lock()
	defer cMtx.Unlock()
	if _, ok := cReservedTimes[eventID]; !ok {
		return 0, false
	}
	if _, ok := cReservedTimes[eventID][sheetID]; !ok {
		return 0, false
	}
	return cReservedTimes[eventID][sheetID], true
}

func GetReservedUserAndTime(eventID int64, sheetID int64) (int64, int64, bool) {
	cMtx.Lock()
	defer cMtx.Unlock()
	if _, ok := cReservedTimes[eventID]; !ok {
		return 0, 0, false
	}
	if _, ok := cReservedTimes[eventID][sheetID]; !ok {
		return 0, 0, false
	}
	if _, ok := cReservedUserID[eventID]; !ok {
		return 0, 0, false
	}
	if _, ok := cReservedUserID[eventID][sheetID]; !ok {
		return 0, 0, false
	}
	return cReservedUserID[eventID][sheetID], cReservedTimes[eventID][sheetID], true
}

func EditEvent(eventID int64, isPublic bool, isClosed bool) {
	cMtx.Lock()
	defer cMtx.Unlock()
	cEvent[eventID].PublicFg = isPublic
	cEvent[eventID].ClosedFg = isClosed
}

func GetEventByID(eventID int64) Event {
	cMtx.Lock()
	defer cMtx.Unlock()
	return *cEvent[eventID]
}

func InitUser() {
	cMtx.Lock()
	defer cMtx.Unlock()
	t := time.Now()

	rows, err := db.Queryx("SELECT * FROM `users`")
	must(err)
	defer rows.Close()
	for rows.Next() {
		var user User
		err = rows.StructScan(&user)
		must(err)

		cUser[user.ID] = &user
		cUserNameToID[user.LoginName] = user.ID
	}
	log.Println("InitUser", time.Since(t).Seconds())
}

// Sheet
func InitSheet() {
	cMtx.Lock()
	defer cMtx.Unlock()
	t := time.Now()

	rows, err := db.Queryx("SELECT * FROM sheets ORDER BY `rank`, num")
	must(err)
	defer rows.Close()
	for rows.Next() {
		var sheet Sheet
		err = rows.StructScan(&sheet)
		must(err)

		cSheet[sheet.ID] = sheet
		cSheetSlice = append(cSheetSlice, sheet)
	}
	log.Println("InitSheet", time.Since(t).Seconds())
}

// Reservation
func InitReservation() {
	cMtx.Lock()
	defer cMtx.Unlock()
	t := time.Now()

	rows, err := db.Queryx("SELECT * FROM reservations")
	must(err)
	defer rows.Close()
	for rows.Next() {
		var r Reservation
		err = rows.StructScan(&r)
		must(err)

		cReservation[r.ID] = &r
	}

	log.Println("InitReservation", time.Since(t).Seconds())
}

func InitReservedTime() {
	cMtx.Lock()
	defer cMtx.Unlock()
	t := time.Now()

	rows, err := db.Queryx("SELECT event_id, sheet_id, user_id, MIN(reserved_at) FROM reservations WHERE canceled_at IS NULL GROUP BY event_id, sheet_id")
	must(err)

	defer rows.Close()
	for rows.Next() {
		var eventId int64
		var sheetId int64
		var userId int64
		var reservedAt *time.Time
		rows.Scan(&eventId, &sheetId, &userId, &reservedAt)
		if _, ok := cReservedTimes[eventId]; !ok {
			cReservedTimes[eventId] = make(map[int64]int64)
			cReservedUserID[eventId] = make(map[int64]int64)
		}
		cReservedTimes[eventId][sheetId] = reservedAt.Unix()
		cReservedUserID[eventId][sheetId] = userId
	}

	log.Println("InitReservedTime", time.Since(t).Seconds())
}

// Event
func InitEvent() {
	cMtx.Lock()
	defer cMtx.Unlock()
	t := time.Now()

	rows, err := db.Queryx("SELECT * FROM events")
	must(err)
	for rows.Next() {
		var e Event
		err = rows.StructScan(&e)
		must(err)

		cEvent[e.ID] = &e
	}

	log.Println("InitEvent", time.Since(t).Seconds())
}

// =======================================

type User struct {
	ID        int64  `json:"id,omitempty" db:"id"`
	Nickname  string `json:"nickname,omitempty" db:"nickname"`
	LoginName string `json:"login_name,omitempty" db:"login_name"`
	PassHash  string `json:"pass_hash,omitempty" db:"pass_hash"`
}

type Event struct {
	ID       int64  `json:"id,omitempty" db:"id"`
	Title    string `json:"title,omitempty" db:"title"`
	PublicFg bool   `json:"public,omitempty" db:"public_fg"`
	ClosedFg bool   `json:"closed,omitempty" db:"closed_fg"`
	Price    int64  `json:"price,omitempty" db:"price"`

	Total   int                `json:"total"`
	Remains int                `json:"remains"`
	Sheets  map[string]*Sheets `json:"sheets,omitempty"`
}

type Sheets struct {
	Total   int      `json:"total" db:"total"`
	Remains int      `json:"remains" db:"remains"`
	Detail  []*Sheet `json:"detail,omitempty" db:"detail"`
	Price   int64    `json:"price" db:"price"`
}

type Sheet struct {
	ID    int64  `json:"-" db:"id"`
	Rank  string `json:"-" db:"rank"`
	Num   int64  `json:"num" db:"num"`
	Price int64  `json:"-" db:"price"`

	Mine           bool       `json:"mine,omitempty"`
	Reserved       bool       `json:"reserved,omitempty"`
	ReservedAt     *time.Time `json:"-"`
	ReservedAtUnix int64      `json:"reserved_at,omitempty"`
}

type Reservation struct {
	ID         int64      `json:"id" db:"id"`
	EventID    int64      `json:"-" db:"event_id"`
	SheetID    int64      `json:"-" db:"sheet_id"`
	UserID     int64      `json:"-" db:"user_id"`
	ReservedAt *time.Time `json:"-" db:"reserved_at"`
	CanceledAt *time.Time `json:"-" db:"canceled_at"`

	Event          *Event `json:"event,omitempty"`
	SheetRank      string `json:"sheet_rank,omitempty"`
	SheetNum       int64  `json:"sheet_num,omitempty"`
	Price          int64  `json:"price,omitempty"`
	ReservedAtUnix int64  `json:"reserved_at,omitempty"`
	CanceledAtUnix int64  `json:"canceled_at,omitempty"`
}

type Administrator struct {
	ID        int64  `json:"id,omitempty"`
	Nickname  string `json:"nickname,omitempty"`
	LoginName string `json:"login_name,omitempty"`
	PassHash  string `json:"pass_hash,omitempty"`
}

func sessUserID(c echo.Context) int64 {
	sess, _ := session.Get("session", c)
	var userID int64
	if x, ok := sess.Values["user_id"]; ok {
		userID, _ = x.(int64)
	}
	return userID
}

func sessSetUserID(c echo.Context, id int64) {
	sess, _ := session.Get("session", c)
	sess.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   3600,
		HttpOnly: true,
	}
	sess.Values["user_id"] = id
	sess.Save(c.Request(), c.Response())
}

func sessDeleteUserID(c echo.Context) {
	sess, _ := session.Get("session", c)
	sess.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   3600,
		HttpOnly: true,
	}
	delete(sess.Values, "user_id")
	sess.Save(c.Request(), c.Response())
}

func sessAdministratorID(c echo.Context) int64 {
	sess, _ := session.Get("session", c)
	var administratorID int64
	if x, ok := sess.Values["administrator_id"]; ok {
		administratorID, _ = x.(int64)
	}
	return administratorID
}

func sessSetAdministratorID(c echo.Context, id int64) {
	sess, _ := session.Get("session", c)
	sess.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   3600,
		HttpOnly: true,
	}
	sess.Values["administrator_id"] = id
	sess.Save(c.Request(), c.Response())
}

func sessDeleteAdministratorID(c echo.Context) {
	sess, _ := session.Get("session", c)
	sess.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   3600,
		HttpOnly: true,
	}
	delete(sess.Values, "administrator_id")
	sess.Save(c.Request(), c.Response())
}

func loginRequired(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if _, err := getLoginUser(c); err != nil {
			return resError(c, "login_required", 401)
		}
		return next(c)
	}
}

func adminLoginRequired(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if _, err := getLoginAdministrator(c); err != nil {
			return resError(c, "admin_login_required", 401)
		}
		return next(c)
	}
}

func getLoginUser(c echo.Context) (*User, error) {
	userID := sessUserID(c)
	if userID == 0 {
		return nil, errors.New("not logged in")
	}
	//var user User
	//err := db.QueryRow("SELECT id, nickname FROM users WHERE id = ?", userID).Scan(&user.ID, &user.Nickname)
	//return &user, err
	user := GetUser(userID)
	return user, nil
}

func getLoginAdministrator(c echo.Context) (*Administrator, error) {
	administratorID := sessAdministratorID(c)
	if administratorID == 0 {
		return nil, errors.New("not logged in")
	}
	var administrator Administrator
	err := db.QueryRow("SELECT id, nickname FROM administrators WHERE id = ?", administratorID).Scan(&administrator.ID, &administrator.Nickname)
	return &administrator, err
}

func getEvents(all bool) ([]*Event, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Commit()

	rows, err := tx.Query("SELECT * FROM events ORDER BY id ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	rows2, err := db.Query("SELECT * FROM sheets ORDER BY `rank`, num")
	if err != nil {
		return nil, err
	}
	defer rows2.Close()
	var sheets []*Sheet
	for rows2.Next() {
		var sheet Sheet
		if err := rows2.Scan(&sheet.ID, &sheet.Rank, &sheet.Num, &sheet.Price); err != nil {
			return nil, err
		}
		sheets = append(sheets, &sheet)
	}

	/*
		reservedTimes := make(map[int64]map[int64]int64)
		rows3, err := db.Query("SELECT event_id, sheet_id, MIN(reserved_at) FROM reservations WHERE canceled_at IS NULL GROUP BY event_id, sheet_id")
		if err != nil {
			return nil, err
		}
		defer rows3.Close()
		for rows3.Next() {
			var eventId int64
			var sheetId int64
			var reservedAt *time.Time
			rows3.Scan(&eventId, &sheetId, &reservedAt)
			if _, ok := reservedTimes[eventId]; !ok {
				reservedTimes[eventId] = make(map[int64]int64)
			}
			reservedTimes[eventId][sheetId] = reservedAt.Unix()
		}
	*/

	var events []*Event
	for rows.Next() {
		var event Event
		if err := rows.Scan(&event.ID, &event.Title, &event.PublicFg, &event.ClosedFg, &event.Price); err != nil {
			return nil, err
		}
		if !all && !event.PublicFg {
			continue
		}
		events = append(events, &event)
	}
	for i := range events {
		events[i].Sheets = map[string]*Sheets{
			"S": &Sheets{},
			"A": &Sheets{},
			"B": &Sheets{},
			"C": &Sheets{},
		}

		for _, v := range sheets {
			sheet := *v
			events[i].Sheets[sheet.Rank].Price = events[i].Price + sheet.Price
			events[i].Total++
			events[i].Sheets[sheet.Rank].Total++

			/*
				if a, ok := reservedTimes[events[i].ID]; ok {
					if b, ok := a[sheet.ID]; ok {
						sheet.Mine = false
						sheet.Reserved = true
						sheet.ReservedAtUnix = b
					} else {
						events[i].Remains++
						events[i].Sheets[sheet.Rank].Remains++
					}
				} else {
					events[i].Remains++
					events[i].Sheets[sheet.Rank].Remains++
				}
			*/
			if a, ok := GetReservedTimes(events[i].ID, sheet.ID); ok {
				sheet.Mine = false
				sheet.Reserved = true
				sheet.ReservedAtUnix = a
			} else {
				events[i].Remains++
				events[i].Sheets[sheet.Rank].Remains++
			}
			events[i].Sheets[sheet.Rank].Detail = append(events[i].Sheets[sheet.Rank].Detail, &sheet)
		}

		if err != nil {
			return nil, err
		}
		for k := range events[i].Sheets {
			events[i].Sheets[k].Detail = nil
		}
	}
	return events, nil
}

func getEvent(eventID, loginUserID int64) (*Event, error) {
	var event Event
	if err := db.QueryRow("SELECT * FROM events WHERE id = ?", eventID).Scan(&event.ID, &event.Title, &event.PublicFg, &event.ClosedFg, &event.Price); err != nil {
		return nil, err
	}
	event.Sheets = map[string]*Sheets{
		"S": &Sheets{},
		"A": &Sheets{},
		"B": &Sheets{},
		"C": &Sheets{},
	}

	rows, err := db.Query("SELECT * FROM sheets ORDER BY `rank`, num")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var sheet Sheet
		if err := rows.Scan(&sheet.ID, &sheet.Rank, &sheet.Num, &sheet.Price); err != nil {
			return nil, err
		}
		event.Sheets[sheet.Rank].Price = event.Price + sheet.Price
		event.Total++
		event.Sheets[sheet.Rank].Total++

		if a, b, ok := GetReservedUserAndTime(eventID, sheet.ID); ok {
			sheet.Mine = a == loginUserID
			sheet.Reserved = true
			sheet.ReservedAtUnix = b
		} else {
			event.Remains++
			event.Sheets[sheet.Rank].Remains++
		}

		event.Sheets[sheet.Rank].Detail = append(event.Sheets[sheet.Rank].Detail, &sheet)
	}

	return &event, nil
}

func getEventNoDetail(eventID int64) (*Event, error) {
	var event Event
	if err := db.QueryRow("SELECT * FROM events WHERE id = ?", eventID).Scan(&event.ID, &event.Title, &event.PublicFg, &event.ClosedFg, &event.Price); err != nil {
		return nil, err
	}
	event.Sheets = map[string]*Sheets{
		"S": &Sheets{},
		"A": &Sheets{},
		"B": &Sheets{},
		"C": &Sheets{},
	}

	rows, err := db.Query("SELECT * FROM sheets ORDER BY `rank`, num")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var sheet Sheet
		if err := rows.Scan(&sheet.ID, &sheet.Rank, &sheet.Num, &sheet.Price); err != nil {
			return nil, err
		}
		event.Sheets[sheet.Rank].Price = event.Price + sheet.Price
		event.Total++
		event.Sheets[sheet.Rank].Total++

		if _, ok := GetReservedTimes(eventID, sheet.ID); !ok {
			event.Remains++
			event.Sheets[sheet.Rank].Remains++
		}
	}

	return &event, nil
}

func getEventNoSheets(eventID int64) (*Event, error) {
	var event Event
	if err := db.QueryRow("SELECT * FROM events WHERE id = ?", eventID).Scan(&event.ID, &event.Title, &event.PublicFg, &event.ClosedFg, &event.Price); err != nil {
		return nil, err
	}
	event.Sheets = map[string]*Sheets{
		"S": &Sheets{},
		"A": &Sheets{},
		"B": &Sheets{},
		"C": &Sheets{},
	}

	rows, err := db.Query("SELECT * FROM sheets ORDER BY `rank`, num")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var sheet Sheet
		if err := rows.Scan(&sheet.ID, &sheet.Rank, &sheet.Num, &sheet.Price); err != nil {
			return nil, err
		}
		event.Sheets[sheet.Rank].Price = event.Price + sheet.Price
	}

	return &event, nil
}

func sanitizeEvent(e *Event) *Event {
	sanitized := *e
	sanitized.Price = 0
	sanitized.PublicFg = false
	sanitized.ClosedFg = false
	return &sanitized
}

func fillinUser(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if user, err := getLoginUser(c); err == nil {
			c.Set("user", user)
		}
		return next(c)
	}
}

func fillinAdministrator(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if administrator, err := getLoginAdministrator(c); err == nil {
			c.Set("administrator", administrator)
		}
		return next(c)
	}
}

func validateRank(rank string) bool {
	var count int
	db.QueryRow("SELECT COUNT(*) FROM sheets WHERE `rank` = ?", rank).Scan(&count)
	return count > 0
}

type Renderer struct {
	templates *template.Template
}

func (r *Renderer) Render(w io.Writer, name string, data interface{}, c echo.Context) error {
	return r.templates.ExecuteTemplate(w, name, data)
}

var db *sqlx.DB

func main() {
	appPort := os.Getenv("APP_PORT")
	if appPort == "" {
		appPort = "8080"
	}

	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true&charset=utf8mb4",
		os.Getenv("DB_USER"), os.Getenv("DB_PASS"),
		os.Getenv("DB_HOST"), os.Getenv("DB_PORT"),
		os.Getenv("DB_DATABASE"),
	)

	var err error
	for {
		db, err = sqlx.Connect("mysql", dsn)
		if err != nil {
			log.Println(err)
			time.Sleep(time.Second)
		}

		for db.Ping() != nil {
			log.Println(err)
			time.Sleep(time.Second)
		}

		break
	}

	InitCache()

	e := echo.New()
	funcs := template.FuncMap{
		"encode_json": func(v interface{}) string {
			b, _ := json.Marshal(v)
			return string(b)
		},
	}
	e.Renderer = &Renderer{
		templates: template.Must(template.New("").Delims("[[", "]]").Funcs(funcs).ParseGlob("views/*.tmpl")),
	}
	e.Use(session.Middleware(sessions.NewCookieStore([]byte("secret"))))
	e.Use(middleware.LoggerWithConfig(middleware.LoggerConfig{Output: os.Stderr}))
	e.Static("/", "public")
	e.GET("/", func(c echo.Context) error {
		events, err := getEvents(false)
		if err != nil {
			return err
		}
		for i, v := range events {
			events[i] = sanitizeEvent(v)
		}
		return c.Render(200, "index.tmpl", echo.Map{
			"events": events,
			"user":   c.Get("user"),
			"origin": c.Scheme() + "://" + c.Request().Host,
		})
	}, fillinUser)
	e.GET("/initialize", func(c echo.Context) error {
		cmd := exec.Command("../../db/init.sh")
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		err := cmd.Run()
		if err != nil {
			return nil
		}

		InitCache()

		noprofile := c.Param("noprofile")
		if noprofile == "" {
			StartProfile(time.Minute)
		}
		return c.NoContent(204)
	})
	e.POST("/api/users", func(c echo.Context) error {
		var params struct {
			Nickname  string `json:"nickname"`
			LoginName string `json:"login_name"`
			Password  string `json:"password"`
		}
		c.Bind(&params)

		tx, err := db.Beginx()
		if err != nil {
			return err
		}

		var user User
		/*
			if err := tx.QueryRow("SELECT * FROM users WHERE login_name = ?", params.LoginName).Scan(&user.ID, &user.LoginName, &user.Nickname, &user.PassHash); err != sql.ErrNoRows {
		*/
		if GetUserByName(params.LoginName) != nil {
			tx.Rollback()
			if err == nil {
				return resError(c, "duplicated", 409)
			}
			return err
		}

		res, err := tx.Exec("INSERT INTO users (login_name, pass_hash, nickname) VALUES (?, SHA2(?, 256), ?)", params.LoginName, params.Password, params.Nickname)
		if err != nil {
			tx.Rollback()
			return resError(c, "", 0)
		}
		userID, err := res.LastInsertId()
		if err != nil {
			tx.Rollback()
			return resError(c, "", 0)
		}

		err = tx.Get(&user, "SELECT * FROM `users` WHERE id = ?", userID)
		if err != nil {
			tx.Rollback()
			return resError(c, "", 0)
		}

		if err := tx.Commit(); err != nil {
			return err
		}

		AddUser(&user)

		return c.JSON(201, echo.Map{
			"id":       userID,
			"nickname": params.Nickname,
		})
	})
	e.GET("/api/users/:id", func(c echo.Context) error {
		uid, err := strconv.Atoi(c.Param("id"))
		if err != nil {
			return err
		}

		/*
			var user User
				if err := db.QueryRow("SELECT id, nickname FROM users WHERE id = ?", c.Param("id")).Scan(&user.ID, &user.Nickname); err != nil {
					return err
				}
		*/
		user := GetUser(int64(uid))

		loginUser, err := getLoginUser(c)
		if err != nil {
			return err
		}
		if user.ID != loginUser.ID {
			return resError(c, "forbidden", 403)
		}

		rows, err := db.Query("SELECT event_id FROM reservations WHERE user_id = ? GROUP BY event_id ORDER BY MAX(IFNULL(canceled_at, reserved_at)) DESC LIMIT 5", user.ID)
		if err != nil {
			return err
		}
		defer rows.Close()

		events := make(map[int64]*Event)
		var recentEvents []*Event
		for rows.Next() {
			var eventID int64
			if err := rows.Scan(&eventID); err != nil {
				return err
			}
			event0, err := getEventNoDetail(eventID)
			if err != nil {
				return err
			}
			events[eventID] = event0
			event := *event0
			recentEvents = append(recentEvents, &event)
		}
		if recentEvents == nil {
			recentEvents = make([]*Event, 0)
		}

		rows, err = db.Query("SELECT r.*, s.rank AS sheet_rank, s.num AS sheet_num FROM reservations r INNER JOIN sheets s ON s.id = r.sheet_id WHERE r.user_id = ? ORDER BY IFNULL(r.canceled_at, r.reserved_at) DESC LIMIT 5", user.ID)
		if err != nil {
			return err
		}
		defer rows.Close()

		var recentReservations []Reservation
		for rows.Next() {
			var reservation Reservation
			var sheet Sheet
			if err := rows.Scan(&reservation.ID, &reservation.EventID, &reservation.SheetID, &reservation.UserID, &reservation.ReservedAt, &reservation.CanceledAt, &sheet.Rank, &sheet.Num); err != nil {
				return err
			}

			event0, ok := events[reservation.EventID]
			if !ok {
				event0, err := getEventNoSheets(reservation.EventID)
				if err != nil {
					return err
				}
				events[reservation.EventID] = event0
			}
			event := *event0
			price := event.Sheets[sheet.Rank].Price
			event.Sheets = nil
			event.Total = 0
			event.Remains = 0

			reservation.Event = &event
			reservation.SheetRank = sheet.Rank
			reservation.SheetNum = sheet.Num
			reservation.Price = price
			reservation.ReservedAtUnix = reservation.ReservedAt.Unix()
			if reservation.CanceledAt != nil {
				reservation.CanceledAtUnix = reservation.CanceledAt.Unix()
			}
			recentReservations = append(recentReservations, reservation)
		}
		if recentReservations == nil {
			recentReservations = make([]Reservation, 0)
		}

		var totalPrice int
		if err := db.QueryRow("SELECT IFNULL(SUM(e.price + s.price), 0) FROM reservations r INNER JOIN sheets s ON s.id = r.sheet_id INNER JOIN events e ON e.id = r.event_id WHERE r.user_id = ? AND r.canceled_at IS NULL", user.ID).Scan(&totalPrice); err != nil {
			return err
		}

		return c.JSON(200, echo.Map{
			"id":                  user.ID,
			"nickname":            user.Nickname,
			"recent_reservations": recentReservations,
			"total_price":         totalPrice,
			"recent_events":       recentEvents,
		})
	}, loginRequired)
	e.POST("/api/actions/login", func(c echo.Context) error {
		var params struct {
			LoginName string `json:"login_name"`
			Password  string `json:"password"`
		}
		c.Bind(&params)

		/*
			user := new(User)
			if err := db.QueryRow("SELECT * FROM users WHERE login_name = ?", params.LoginName).Scan(&user.ID, &user.LoginName, &user.Nickname, &user.PassHash); err != nil {
				if err == sql.ErrNoRows {
					return resError(c, "authentication_failed", 401)
				}
				return err
			}
		*/
		user := GetUserByName(params.LoginName)
		if user == nil {
			return resError(c, "authentication_failed", 401)
		}

		var passHash string
		if err := db.QueryRow("SELECT SHA2(?, 256)", params.Password).Scan(&passHash); err != nil {
			return err
		}
		if user.PassHash != passHash {
			return resError(c, "authentication_failed", 401)
		}

		sessSetUserID(c, user.ID)
		user, err = getLoginUser(c)
		if err != nil {
			return err
		}
		return c.JSON(200, user)
	})
	e.POST("/api/actions/logout", func(c echo.Context) error {
		sessDeleteUserID(c)
		return c.NoContent(204)
	}, loginRequired)
	e.GET("/api/events", func(c echo.Context) error {
		events, err := getEvents(true)
		if err != nil {
			return err
		}
		for i, v := range events {
			events[i] = sanitizeEvent(v)
		}
		return c.JSON(200, events)
	})
	e.GET("/api/events/:id", func(c echo.Context) error {
		eventID, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil {
			return resError(c, "not_found", 404)
		}

		loginUserID := int64(-1)
		if user, err := getLoginUser(c); err == nil {
			loginUserID = user.ID
		}

		event, err := getEvent(eventID, loginUserID)
		if err != nil {
			if err == sql.ErrNoRows {
				return resError(c, "not_found", 404)
			}
			return err
		} else if !event.PublicFg {
			return resError(c, "not_found", 404)
		}
		return c.JSON(200, sanitizeEvent(event))
	})
	e.POST("/api/events/:id/actions/reserve", func(c echo.Context) error {
		reqMtx.Lock()
		defer reqMtx.Unlock()

		eventID, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil {
			return resError(c, "not_found", 404)
		}
		var params struct {
			Rank string `json:"sheet_rank"`
		}
		c.Bind(&params)

		user, err := getLoginUser(c)
		if err != nil {
			return err
		}

		event, err := getEvent(eventID, user.ID)
		if err != nil {
			if err == sql.ErrNoRows {
				return resError(c, "invalid_event", 404)
			}
			return err
		} else if !event.PublicFg {
			return resError(c, "invalid_event", 404)
		}

		if !validateRank(params.Rank) {
			return resError(c, "invalid_rank", 400)
		}

		var sheet Sheet
		var reservationID int64
		for {
			if err := db.QueryRow("SELECT * FROM sheets WHERE id NOT IN (SELECT sheet_id FROM reservations WHERE event_id = ? AND canceled_at IS NULL) AND `rank` = ? ORDER BY RAND() LIMIT 1", event.ID, params.Rank).Scan(&sheet.ID, &sheet.Rank, &sheet.Num, &sheet.Price); err != nil {
				if err == sql.ErrNoRows {
					return resError(c, "sold_out", 409)
				}
				return err
			}

			tx, err := db.Beginx()
			if err != nil {
				return err
			}

			res, err := tx.Exec("INSERT INTO reservations (event_id, sheet_id, user_id, reserved_at) VALUES (?, ?, ?, ?)", event.ID, sheet.ID, user.ID, time.Now().UTC().Format("2006-01-02 15:04:05.000000"))
			if err != nil {
				tx.Rollback()
				log.Println("re-try: rollback by", err)
				continue
			}
			reservationID, err = res.LastInsertId()
			if err != nil {
				tx.Rollback()
				log.Println("re-try: rollback by", err)
				continue
			}

			var reserv Reservation
			err = tx.Get(&reserv, "SELECT * FROM `reservations` WHERE id = ?", reservationID)
			if err != nil {
				tx.Rollback()
				log.Println("re-try: rollback by", err)
				continue
			}

			if err := tx.Commit(); err != nil {
				tx.Rollback()
				log.Println("re-try: rollback by", err)
				continue
			}

			AddReservation(&reserv)

			break
		}
		return c.JSON(202, echo.Map{
			"id":         reservationID,
			"sheet_rank": params.Rank,
			"sheet_num":  sheet.Num,
		})
	}, loginRequired)
	e.DELETE("/api/events/:id/sheets/:rank/:num/reservation", func(c echo.Context) error {
		reqMtx.Lock()
		defer reqMtx.Unlock()

		eventID, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil {
			return resError(c, "not_found", 404)
		}
		rank := c.Param("rank")
		num := c.Param("num")

		user, err := getLoginUser(c)
		if err != nil {
			return err
		}

		event, err := getEvent(eventID, user.ID)
		if err != nil {
			if err == sql.ErrNoRows {
				return resError(c, "invalid_event", 404)
			}
			return err
		} else if !event.PublicFg {
			return resError(c, "invalid_event", 404)
		}

		if !validateRank(rank) {
			return resError(c, "invalid_rank", 404)
		}

		var sheet Sheet
		if err := db.QueryRow("SELECT * FROM sheets WHERE `rank` = ? AND num = ?", rank, num).Scan(&sheet.ID, &sheet.Rank, &sheet.Num, &sheet.Price); err != nil {
			if err == sql.ErrNoRows {
				return resError(c, "invalid_sheet", 404)
			}
			return err
		}

		tx, err := db.Begin()
		if err != nil {
			return err
		}

		// TODO SELECT消したい
		var reservation Reservation
		if err := tx.QueryRow("SELECT * FROM reservations WHERE event_id = ? AND sheet_id = ? AND canceled_at IS NULL GROUP BY event_id HAVING reserved_at = MIN(reserved_at)", event.ID, sheet.ID).Scan(&reservation.ID, &reservation.EventID, &reservation.SheetID, &reservation.UserID, &reservation.ReservedAt, &reservation.CanceledAt); err != nil {
			tx.Rollback()
			if err == sql.ErrNoRows {
				return resError(c, "not_reserved", 400)
			}
			return err
		}
		if reservation.UserID != user.ID {
			tx.Rollback()
			return resError(c, "not_permitted", 403)
		}

		cancelTime := time.Now().UTC()
		if _, err := tx.Exec("UPDATE reservations SET canceled_at = ? WHERE id = ?", cancelTime.Format("2006-01-02 15:04:05.000000"), reservation.ID); err != nil {
			tx.Rollback()
			return err
		}

		if err := tx.Commit(); err != nil {
			return err
		}

		CancelReservation(GetReservation(reservation.ID), cancelTime)

		return c.NoContent(204)
	}, loginRequired)
	e.GET("/admin/", func(c echo.Context) error {
		var events []*Event
		administrator := c.Get("administrator")
		if administrator != nil {
			var err error
			if events, err = getEvents(true); err != nil {
				return err
			}
		}
		return c.Render(200, "admin.tmpl", echo.Map{
			"events":        events,
			"administrator": administrator,
			"origin":        c.Scheme() + "://" + c.Request().Host,
		})
	}, fillinAdministrator)
	e.POST("/admin/api/actions/login", func(c echo.Context) error {
		var params struct {
			LoginName string `json:"login_name"`
			Password  string `json:"password"`
		}
		c.Bind(&params)

		administrator := new(Administrator)
		if err := db.QueryRow("SELECT * FROM administrators WHERE login_name = ?", params.LoginName).Scan(&administrator.ID, &administrator.LoginName, &administrator.Nickname, &administrator.PassHash); err != nil {
			if err == sql.ErrNoRows {
				return resError(c, "authentication_failed", 401)
			}
			return err
		}

		var passHash string
		if err := db.QueryRow("SELECT SHA2(?, 256)", params.Password).Scan(&passHash); err != nil {
			return err
		}
		if administrator.PassHash != passHash {
			return resError(c, "authentication_failed", 401)
		}

		sessSetAdministratorID(c, administrator.ID)
		administrator, err = getLoginAdministrator(c)
		if err != nil {
			return err
		}
		return c.JSON(200, administrator)
	})
	e.POST("/admin/api/actions/logout", func(c echo.Context) error {
		sessDeleteAdministratorID(c)
		return c.NoContent(204)
	}, adminLoginRequired)
	e.GET("/admin/api/events", func(c echo.Context) error {
		events, err := getEvents(true)
		if err != nil {
			return err
		}
		return c.JSON(200, events)
	}, adminLoginRequired)
	e.POST("/admin/api/events", func(c echo.Context) error {
		var params struct {
			Title  string `json:"title"`
			Public bool   `json:"public"`
			Price  int    `json:"price"`
		}
		c.Bind(&params)

		tx, err := db.Begin()
		if err != nil {
			return err
		}

		res, err := tx.Exec("INSERT INTO events (title, public_fg, closed_fg, price) VALUES (?, ?, 0, ?)", params.Title, params.Public, params.Price)
		if err != nil {
			tx.Rollback()
			return err
		}
		eventID, err := res.LastInsertId()
		if err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}

		event, err := getEvent(eventID, -1)
		if err != nil {
			return err
		}
		return c.JSON(200, event)
	}, adminLoginRequired)
	e.GET("/admin/api/events/:id", func(c echo.Context) error {
		eventID, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil {
			return resError(c, "not_found", 404)
		}
		event, err := getEvent(eventID, -1)
		if err != nil {
			if err == sql.ErrNoRows {
				return resError(c, "not_found", 404)
			}
			return err
		}
		return c.JSON(200, event)
	}, adminLoginRequired)
	e.POST("/admin/api/events/:id/actions/edit", func(c echo.Context) error {
		eventID, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil {
			return resError(c, "not_found", 404)
		}

		var params struct {
			Public bool `json:"public"`
			Closed bool `json:"closed"`
		}
		c.Bind(&params)
		if params.Closed {
			params.Public = false
		}

		event, err := getEvent(eventID, -1)
		if err != nil {
			if err == sql.ErrNoRows {
				return resError(c, "not_found", 404)
			}
			return err
		}

		if event.ClosedFg {
			return resError(c, "cannot_edit_closed_event", 400)
		} else if event.PublicFg && params.Closed {
			return resError(c, "cannot_close_public_event", 400)
		}

		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec("UPDATE events SET public_fg = ?, closed_fg = ? WHERE id = ?", params.Public, params.Closed, event.ID); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}

		e, err := getEvent(eventID, -1)
		if err != nil {
			return err
		}
		c.JSON(200, e)
		return nil
	}, adminLoginRequired)
	e.GET("/admin/api/reports/events/:id/sales", func(c echo.Context) error {
		eventID, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil {
			return resError(c, "not_found", 404)
		}

		rows, err := db.Query("SELECT r.*, s.rank AS sheet_rank, s.num AS sheet_num, s.price AS sheet_price, e.price AS event_price FROM reservations r INNER JOIN sheets s ON s.id = r.sheet_id INNER JOIN events e ON e.id = r.event_id WHERE r.event_id = ?", eventID)
		if err != nil {
			return err
		}
		defer rows.Close()

		var reports []Report
		for rows.Next() {
			var reservation Reservation
			var sheet Sheet
			var eventPrice int64
			if err := rows.Scan(&reservation.ID, &reservation.EventID, &reservation.SheetID, &reservation.UserID, &reservation.ReservedAt, &reservation.CanceledAt, &sheet.Rank, &sheet.Num, &sheet.Price, &eventPrice); err != nil {
				return err
			}
			report := Report{
				ReservationID: reservation.ID,
				EventID:       eventID,
				Rank:          sheet.Rank,
				Num:           sheet.Num,
				UserID:        reservation.UserID,
				SoldAt:        reservation.ReservedAt.Format("2006-01-02T15:04:05.000000Z"),
				Price:         eventPrice + sheet.Price,
				ReservedAt:    reservation.ReservedAt.Unix(),
			}
			if reservation.CanceledAt != nil {
				report.CanceledAt = reservation.CanceledAt.Format("2006-01-02T15:04:05.000000Z")
			}
			reports = append(reports, report)
		}
		return renderReportCSV(c, reports)
	}, adminLoginRequired)
	e.GET("/admin/api/reports/sales", func(c echo.Context) error {
		rows, err := db.Query("select r.*, s.rank as sheet_rank, s.num as sheet_num, s.price as sheet_price, e.id as event_id, e.price as event_price from reservations r inner join sheets s on s.id = r.sheet_id inner join events e on e.id = r.event_id")
		if err != nil {
			return err
		}
		defer rows.Close()

		var reports []Report
		for rows.Next() {
			var reservation Reservation
			var sheet Sheet
			var event Event
			if err := rows.Scan(&reservation.ID, &reservation.EventID, &reservation.SheetID, &reservation.UserID, &reservation.ReservedAt, &reservation.CanceledAt, &sheet.Rank, &sheet.Num, &sheet.Price, &event.ID, &event.Price); err != nil {
				return err
			}
			report := Report{
				ReservationID: reservation.ID,
				EventID:       event.ID,
				Rank:          sheet.Rank,
				Num:           sheet.Num,
				UserID:        reservation.UserID,
				SoldAt:        reservation.ReservedAt.Format("2006-01-02T15:04:05.000000Z"),
				Price:         event.Price + sheet.Price,
				ReservedAt:    reservation.ReservedAt.Unix(),
			}
			if reservation.CanceledAt != nil {
				report.CanceledAt = reservation.CanceledAt.Format("2006-01-02T15:04:05.000000Z")
			}
			reports = append(reports, report)
		}
		return renderReportCSV(c, reports)
	}, adminLoginRequired)

	e.Start(":" + appPort)
}

type Report struct {
	ReservationID int64
	EventID       int64
	Rank          string
	Num           int64
	UserID        int64
	SoldAt        string
	CanceledAt    string
	Price         int64
	ReservedAt    int64
}

func renderReportCSV(c echo.Context, reports []Report) error {
	sort.Slice(reports, func(i, j int) bool { return reports[i].ReservedAt < reports[j].ReservedAt })

	body := bytes.NewBufferString("reservation_id,event_id,rank,num,price,user_id,sold_at,canceled_at\n")
	for _, v := range reports {
		body.WriteString(fmt.Sprintf("%d,%d,%s,%d,%d,%d,%s,%s\n",
			v.ReservationID, v.EventID, v.Rank, v.Num, v.Price, v.UserID, v.SoldAt, v.CanceledAt))
	}

	c.Response().Header().Set("Content-Type", `text/csv; charset=UTF-8`)
	c.Response().Header().Set("Content-Disposition", `attachment; filename="report.csv"`)
	_, err := io.Copy(c.Response(), body)
	return err
}

func resError(c echo.Context, e string, status int) error {
	if e == "" {
		e = "unknown"
	}
	if status < 100 {
		status = 500
	}
	return c.JSON(status, map[string]string{"error": e})
}
