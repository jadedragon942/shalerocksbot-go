package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
	irc "github.com/thoj/go-ircevent"
)

const debug = false
const version = "0.03"

/*********************************************************************
 * 1) Types, Structures, and Global Variables
 *********************************************************************/

// For the animal hunt
type currentAnimalState struct {
	animal  string
	spawned bool
	claimed bool
}

var (
	animalMu     sync.Mutex
	activeAnimal currentAnimalState

	db      *sql.DB
	bot     *irc.Connection
	channel = ""
)

/*********************************************************************
 * 2) Badge-Related Data and Functions
 *********************************************************************/

type badgeCommand struct {
	action string // "add", "delete", or "show"
	name   string
	date   string
}

func parseBadgeCommand(message string) (*badgeCommand, error) {
	if !strings.HasPrefix(message, ";badge") {
		return nil, fmt.Errorf("not a ;badge command")
	}

	if strings.Contains(message, "-add") {
		nameRegex := regexp.MustCompile(`-name="([^"]+)"`)
		nameMatch := nameRegex.FindStringSubmatch(message)
		if len(nameMatch) < 2 {
			return nil, fmt.Errorf("missing -name= for add")
		}

		dateRegex := regexp.MustCompile(`-date="([^"]+)"`)
		dateMatch := dateRegex.FindStringSubmatch(message)
		if len(dateMatch) < 2 {
			return nil, fmt.Errorf("missing -date= for add")
		}

		return &badgeCommand{
			action: "add",
			name:   nameMatch[1],
			date:   dateMatch[1],
		}, nil

	} else if strings.Contains(message, "-delete") {
		nameRegex := regexp.MustCompile(`-name="([^"]+)"`)
		nameMatch := nameRegex.FindStringSubmatch(message)
		if len(nameMatch) < 2 {
			return nil, fmt.Errorf("missing -name= for delete")
		}

		return &badgeCommand{
			action: "delete",
			name:   nameMatch[1],
		}, nil

	} else {
		return &badgeCommand{action: "show"}, nil
	}
}

func parseOrConvertDate(dateStr string) string {
	dateStr = strings.ToLower(strings.TrimSpace(dateStr))

	if dateStr == "today" {
		return time.Now().Format(time.RFC3339)
	}

	daysAgoRegex := regexp.MustCompile(`^(\d+)\s+days\s+ago$`)
	if match := daysAgoRegex.FindStringSubmatch(dateStr); len(match) == 2 {
		daysInt, err := strconv.Atoi(match[1])
		if err == nil {
			return time.Now().AddDate(0, 0, -daysInt).Format(time.RFC3339)
		}
	}

	if t, err := time.Parse(time.RFC3339, dateStr); err == nil {
		return t.Format(time.RFC3339)
	}
	return dateStr
}

func daysSince(dateStr string) int {
	t, err := time.Parse(time.RFC3339, dateStr)
	if err != nil {
		return 0
	}
	diff := time.Since(t)
	if diff < 0 {
		return 0
	}
	return int(diff.Hours() / 24)
}

/*********************************************************************
 * 3) Weather Functions (OpenWeatherMap)
 *********************************************************************/
var httpClient = &http.Client{Timeout: 10 * time.Second}

// For parsing Nominatim's JSON response
type nominatimResponse []struct {
	Lat string `json:"lat"`
	Lon string `json:"lon"`
	// You can add more fields if needed (e.g., display_name)
}

func geocodeViaNominatim(query string) (float64, float64, error) {
	baseURL := "https://nominatim.openstreetmap.org/search"
	params := url.Values{}
	params.Set("q", query)
	params.Set("format", "json")

	reqURL := fmt.Sprintf("%s?%s", baseURL, params.Encode())
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return 0, 0, err
	}
	// IMPORTANT: set a custom User-Agent per Nominatim policy
	req.Header.Set("User-Agent", "shalerocksbot-go/"+version+" (djade942@gmail.com)")

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return 0, 0, fmt.Errorf("nominatim error status %d: %s",
			resp.StatusCode, string(bodyBytes))
	}

	var results nominatimResponse
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return 0, 0, err
	}

	if len(results) == 0 {
		return 0, 0, fmt.Errorf("no geocoding results for %q", query)
	}

	lat, err := strconv.ParseFloat(results[0].Lat, 64)
	if err != nil {
		return 0, 0, err
	}
	lon, err := strconv.ParseFloat(results[0].Lon, 64)
	if err != nil {
		return 0, 0, err
	}

	return lat, lon, nil
}

func fetchWeatherSummary25(location string) (string, error) {
	apiKey := os.Getenv("OWM_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("no OWM_API_KEY found in environment")
	}

	query := url.QueryEscape(location)
	apiURL := fmt.Sprintf("https://api.openweathermap.org/data/2.5/weather?q=%s&units=imperial&appid=%s",
		query, apiKey,
	)

	resp, err := httpClient.Get(apiURL)
	if err != nil {
		return "", fmt.Errorf("failed to get weather data: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var data struct {
		Name    string `json:"name"`
		Weather []struct {
			Description string `json:"description"`
		} `json:"weather"`
		Main struct {
			Temp float64 `json:"temp"`
		} `json:"main"`
		Sys struct {
			Country string `json:"country"`
		} `json:"sys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", fmt.Errorf("failed to decode JSON: %v", err)
	}

	if data.Name == "" && len(data.Weather) == 0 {
		return "", fmt.Errorf("no weather info found for '%s'", location)
	}

	desc := "unknown"
	if len(data.Weather) > 0 {
		desc = data.Weather[0].Description
	}
	return fmt.Sprintf("It's %.1f°F with %s in %s, %s.",
		data.Main.Temp, desc, data.Name, data.Sys.Country), nil
}

func fetchWeatherSummary3(location string) (string, error) {
	// 1) Get your API key from env
	apiKey := os.Getenv("OWM_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("no OWM_API_KEY found in environment")
	}

	// 2) Geocode the user-provided location via Nominatim
	lat, lon, err := geocodeViaNominatim(location)
	if err != nil {
		return "", fmt.Errorf("failed to geocode %q: %v", location, err)
	}

	// 3) Call OpenWeatherMap One Call 3.0 API
	//    We request current weather only, so we can use `&exclude=minutely,hourly,daily,alerts`
	//    if we only want "current" data. You can remove that param if you want forecasts.
	oneCallURL := fmt.Sprintf(
		"https://api.openweathermap.org/data/3.0/onecall?lat=%f&lon=%f&exclude=minutely,hourly,daily,alerts&units=imperial&appid=%s",
		lat, lon, apiKey,
	)

	resp, err := httpClient.Get(oneCallURL)
	if err != nil {
		return "", fmt.Errorf("failed to get weather data: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("OWM error status %d: %s",
			resp.StatusCode, string(bodyBytes))
	}

	// 4) Parse OWM One Call response
	var owmData struct {
		Lat     float64 `json:"lat"`
		Lon     float64 `json:"lon"`
		Current struct {
			Temp    float64 `json:"temp"`
			Weather []struct {
				Description string `json:"description"`
			} `json:"weather"`
		} `json:"current"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&owmData); err != nil {
		return "", fmt.Errorf("failed to decode OWM JSON: %v", err)
	}

	if len(owmData.Current.Weather) == 0 {
		return "", fmt.Errorf("no weather data in OWM response for %q", location)
	}

	// 5) Build a summary. You could display lat/lon or the original `location` string, etc.
	desc := owmData.Current.Weather[0].Description
	tempF := owmData.Current.Temp
	summary := fmt.Sprintf("It's %.1f°F with %s in %s (%.4f, %.4f).",
		tempF, desc, location, owmData.Lat, owmData.Lon)

	return summary, nil
}

/*********************************************************************
 * 4) Animal Hunt Logic
 *********************************************************************/
const brown = "\x0305"
const normal = "\x0f"
const bold  = "\x02"
const pink = "\x0313"

var possibleAnimals = []struct {
	name  string
	sound string
}{
	{"duck", brown+"(o)<  ・゜゜・。。・゜゜HONK"+normal},
	{"pig", brown+"~~(_ _)^"+pink+":"+brown+" OINK" + normal},
	{"seal", bold+"(ᵔᴥᵔ) BARK"+normal},
	{"mouse", brown+"<:3)~ SQEEK"+normal},
	{"shark", bold+"____/\\_______\\o/___ AHHHH! SHARK"+normal},
}


func scheduleNextAnimal() {
	go func() {
		delay := rand.Intn(3180) + 360 // 30..300
		if debug {
			delay = 8 // 8 seconds when in debug
		}
		time.Sleep(time.Duration(delay) * time.Second)
		spawnAnimal()
	}()
}

func spawnAnimal() {
	animalMu.Lock()
	defer animalMu.Unlock()

	scheduleNextAnimal()

	idx := rand.Intn(len(possibleAnimals))
	chosen := possibleAnimals[idx]

	activeAnimal = currentAnimalState{
		animal:  chosen.name,
		spawned: true,
		claimed: false,
	}
	bot.Privmsg(channel, fmt.Sprintf("%s", chosen.sound))
}

func recordAnimalHunt(nick, animal, action string) error {
	_, err := db.Exec(`
		INSERT INTO animalhunt (nick, animal, action, date)
		VALUES (?, ?, ?, datetime('now'))
	`, nick, animal, action)
	return err
}

func getHuntStats(nick string) (befCount, shotCount int, err error) {
	row := db.QueryRow(`
		SELECT COUNT(*) FROM animalhunt 
		WHERE nick = ? AND action = 'befriend'
	`, nick)
	if err = row.Scan(&befCount); err != nil {
		return
	}
	row = db.QueryRow(`
		SELECT COUNT(*) FROM animalhunt 
		WHERE nick = ? AND action = 'shoot'
	`, nick)
	err = row.Scan(&shotCount)
	return
}

/*********************************************************************
 * 5) "tell" Command Logic
 *********************************************************************/
func storeTell(targetNick, fromNick, message string) error {
	_, err := db.Exec(`
		INSERT INTO pending_tells (targetNick, fromNick, message, date)
		VALUES (?, ?, ?, datetime('now'))
	`, targetNick, fromNick, message)
	return err
}

func deliverTells(nick string) {
	rows, err := db.Query(`
		SELECT id, fromNick, message 
		FROM pending_tells 
		WHERE targetNick = ?
		ORDER BY id
	`, nick)
	if err != nil {
		log.Printf("[ERROR] deliverTells query: %v", err)
		return
	}
	defer rows.Close()

	var idsToDelete []int
	for rows.Next() {
		var id int
		var fromNick, msg string
		if err := rows.Scan(&id, &fromNick, &msg); err != nil {
			log.Printf("[ERROR] deliverTells scan: %v", err)
			continue
		}
		bot.Privmsg(channel, fmt.Sprintf("%s, %s said: %s", nick, fromNick, msg))
		idsToDelete = append(idsToDelete, id)
	}

	rows.Close()
	if len(idsToDelete) == 0 {
		return
	}
	for _, idVal := range idsToDelete {
		if _, err := db.Exec(`DELETE FROM pending_tells WHERE id = ?`, idVal); err != nil {
			log.Printf("[ERROR] deliverTells delete: %v", err)
		}
	}
}

/*********************************************************************
 * 6) Points System (";addpoint" / ";rmpoint")
 *********************************************************************/
// Single `points` column. ;addpoint => points++, ;rmpoint => points--

func initOrGetPoints(fromNick, toNick string) (int, error) {
	row := db.QueryRow(`
		SELECT points 
		FROM user_points 
		WHERE fromNick = ? AND toNick = ?
	`, fromNick, toNick)

	var current int
	err := row.Scan(&current)
	if err == sql.ErrNoRows {
		// Insert a new row with points=0
		_, err2 := db.Exec(`
			INSERT INTO user_points (fromNick, toNick, points)
			VALUES (?, ?, 0)
		`, fromNick, toNick)
		if err2 != nil {
			return 0, err2
		}
		return 0, nil
	} else if err != nil {
		return 0, err
	}
	return current, nil
}

func addPoint(fromNick, toNick string) (int, error) {
	// ensure row exists
	current, err := initOrGetPoints(fromNick, toNick)
	if err != nil {
		return 0, err
	}
	// increment
	_, err = db.Exec(`
		UPDATE user_points 
		SET points = points + 1
		WHERE fromNick = ? AND toNick = ?
	`, fromNick, toNick)
	if err != nil {
		return 0, err
	}
	// get new total
	row := db.QueryRow(`
		SELECT points FROM user_points 
		WHERE fromNick = ? AND toNick = ?
	`, fromNick, toNick)
	if err2 := row.Scan(&current); err2 != nil {
		return 0, err2
	}
	return current, nil
}

func removePoint(fromNick, toNick string) (int, error) {
	current, err := initOrGetPoints(fromNick, toNick)
	if err != nil {
		return 0, err
	}
	_, err = db.Exec(`
		UPDATE user_points 
		SET points = points - 1
		WHERE fromNick = ? AND toNick = ?
	`, fromNick, toNick)
	if err != nil {
		return 0, err
	}
	row := db.QueryRow(`
		SELECT points FROM user_points 
		WHERE fromNick = ? AND toNick = ?
	`, fromNick, toNick)
	if err2 := row.Scan(&current); err2 != nil {
		return 0, err2
	}
	return current, nil
}

/*********************************************************************
 * 7) Main
 *********************************************************************/

func main() {
	rand.Seed(time.Now().UnixNano())

	var err error
	log.Println("[DEBUG] Opening/creating badges.db SQLite database.")
	db, err = sql.Open("sqlite3", "badges.db")
	if err != nil {
		log.Fatalf("[FATAL] Failed to open database: %v", err)
	}
	defer db.Close()

	// 1) Create badges table
	createTableSQL := `
	CREATE TABLE IF NOT EXISTS badges (
	    id   INTEGER PRIMARY KEY AUTOINCREMENT,
	    name TEXT NOT NULL,
	    date TEXT NOT NULL,
	    nick TEXT NOT NULL,
	    UNIQUE(nick, name)
	);
	`
	log.Println("[DEBUG] Ensuring badges table exists.")
	if _, err := db.Exec(createTableSQL); err != nil {
		log.Fatalf("[FATAL] Failed to create badges table: %v", err)
	}

	// 2) Create animalhunt table
	createHuntTableSQL := `
	CREATE TABLE IF NOT EXISTS animalhunt (
	    id     INTEGER PRIMARY KEY AUTOINCREMENT,
	    nick   TEXT NOT NULL,
	    animal TEXT NOT NULL,
	    action TEXT NOT NULL,
	    date   TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	`
	log.Println("[DEBUG] Ensuring animalhunt table exists.")
	if _, err := db.Exec(createHuntTableSQL); err != nil {
		log.Fatalf("[FATAL] Failed to create animalhunt table: %v", err)
	}

	// 3) Create pending_tells table
	createTellsTableSQL := `
	CREATE TABLE IF NOT EXISTS pending_tells (
	    id         INTEGER PRIMARY KEY AUTOINCREMENT,
	    targetNick TEXT NOT NULL,
	    fromNick   TEXT NOT NULL,
	    message    TEXT NOT NULL,
	    date       TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	`
	log.Println("[DEBUG] Ensuring pending_tells table exists.")
	if _, err := db.Exec(createTellsTableSQL); err != nil {
		log.Fatalf("[FATAL] Failed to create pending_tells table: %v", err)
	}

	// 4) Create user_points with a single "points" column
	createPointsTableSQL := `
	CREATE TABLE IF NOT EXISTS user_points (
	    id        INTEGER PRIMARY KEY AUTOINCREMENT,
	    fromNick  TEXT NOT NULL,
	    toNick    TEXT NOT NULL,
	    points    INTEGER NOT NULL DEFAULT 0,
	    UNIQUE(fromNick, toNick)
	);
	`
	log.Println("[DEBUG] Ensuring user_points table exists.")
	if _, err := db.Exec(createPointsTableSQL); err != nil {
		log.Fatalf("[FATAL] Failed to create user_points table: %v", err)
	}

	// IRC Config
	nickServPass := os.Getenv("NICKSERV_PASS")
	nick := os.Getenv("NICKNAME")
	if nick == "" {
		nick = "jadebot"
	}
	user := nick
	server := "irc.snoonet.org:6667"
	channel = os.Getenv("CHANNEL")
	if channel == "" {
		channel = "#jadebotdev"
	}

	log.Printf("[DEBUG] Configuring IRC bot. Nick: %s, Server: %s, Channel: %s\n",
		nick, server, channel)

	bot = irc.IRC(nick, user)
	bot.Server = server
	bot.Debug = true

	log.Println("[DEBUG] Attempting to connect to IRC server...")
	if err := bot.Connect(bot.Server); err != nil {
		log.Fatalf("[FATAL] Failed to connect to IRC server: %v", err)
	}

	// IRC Callbacks
	bot.AddCallback("*", func(e *irc.Event) {
		log.Printf("[IRC EVENT] Code: %s | Source: %s | Args: %v | Raw: %s",
			e.Code, e.Source, e.Arguments, e.Raw)
	})
	bot.AddCallback("001", func(e *irc.Event) {
		log.Printf("[DEBUG] Received RPL_WELCOME: %s", e.Raw)
		if nickServPass != "" {
			log.Printf("[DEBUG] Sending NickServ IDENTIFY.")
			bot.Privmsgf("NickServ", "IDENTIFY %s", nickServPass)
		} else {
			log.Printf("[DEBUG] No NickServ password provided; skipping IDENTIFY.")
		}
		log.Printf("[DEBUG] Joining channel %s now.", channel)
		bot.Join(channel)

		// Start the animal-hunt cycle
		scheduleNextAnimal()
	})

	// Main PRIVMSG callback
	bot.AddCallback("PRIVMSG", func(e *irc.Event) {
		msg := e.Message()
		userNick := e.Nick

		// deliver any waiting ;tell messages
		deliverTells(userNick)

		// 1) ;weather
		if strings.HasPrefix(strings.ToLower(msg), ";weather") {
			parts := strings.SplitN(msg, " ", 2)
			if len(parts) < 2 {
				bot.Privmsg(channel, "Usage: ;weather <location>")
				return
			}
			location := strings.TrimSpace(parts[1])
			if location == "" {
				bot.Privmsg(channel, "Usage: ;weather <location>")
				return
			}
			go func() {
				if os.Getenv("OWM_V25") != "" {
					summary, err := fetchWeatherSummary25(location)
					if err != nil {
						bot.Privmsg(channel, fmt.Sprintf("Could not get weather for '%s': %v", location, err))
					} else {
						bot.Privmsg(channel, summary)
					}
				} else {
					summary, err := fetchWeatherSummary3(location)
					if err != nil {
						bot.Privmsg(channel, fmt.Sprintf("Could not get weather for '%s': %v", location, err))
					} else {
						bot.Privmsg(channel, summary)
					}

				}
			}()
			return
		}

		// 2) ;ask
		if strings.HasPrefix(strings.ToLower(msg), ";ask") {
			raw := strings.TrimSpace(msg[len(";ask"):])
			if !strings.Contains(raw, " or ") {
				bot.Privmsg(channel, "perhaps")
				return
			}
			options := strings.Split(raw, " or ")
			var cleaned []string
			for _, opt := range options {
				opt = strings.TrimSpace(opt)
				if opt != "" {
					cleaned = append(cleaned, opt)
				}
			}
			if len(cleaned) == 0 {
				bot.Privmsg(channel, "perhaps")
				return
			}
			choice := cleaned[rand.Intn(len(cleaned))]
			bot.Privmsg(channel, choice)
			return
		}

		// 3) Animal Hunt: ;bef or ;bang
		cmdLower := strings.ToLower(msg)
		if cmdLower == ";bef" || cmdLower == ";bang" {
			animalMu.Lock()
			defer animalMu.Unlock()

			if !activeAnimal.spawned || activeAnimal.claimed {
				bot.Privmsg(channel, "There was no animal, sowwy!")
				return
			}
			activeAnimal.claimed = true

			theAnimal := activeAnimal.animal
			var action string
			if cmdLower == ";bef" {
				action = "befriend"
			} else {
				action = "shoot"
			}
			if err := recordAnimalHunt(userNick, theAnimal, action); err != nil {
				log.Printf("[ERROR] recordAnimalHunt failed: %v", err)
				bot.Privmsg(channel, fmt.Sprintf("Database error: %v", err))
				return
			}
			befCount, shotCount, _ := getHuntStats(userNick)
			if action == "befriend" {
				bot.Privmsg(channel,
					fmt.Sprintf("%s befriended the %s! You have now befriended %d and shot %d.",
						userNick, theAnimal, befCount, shotCount))
			} else {
				bot.Privmsg(channel,
					fmt.Sprintf("%s shot the %s! You have now shot %d and befriended %d.",
						userNick, theAnimal, shotCount, befCount))
			}
			return
		}

		// Optional ;huntscore
		if strings.HasPrefix(cmdLower, ";huntscore") {
			befCount, shotCount, err := getHuntStats(userNick)
			if err != nil {
				bot.Privmsg(channel, fmt.Sprintf("Error fetching your hunt score: %v", err))
				return
			}
			bot.Privmsg(channel,
				fmt.Sprintf("%s's hunt stats: befriended %d, shot %d.", userNick, befCount, shotCount))
			return
		}

		// 4) ;tell
		if strings.HasPrefix(strings.ToLower(msg), ";tell") {
			parts := strings.SplitN(msg, " ", 3)
			if len(parts) < 3 {
				bot.Privmsg(channel, "Usage: ;tell <username> <message>")
				return
			}
			targetNick := strings.TrimSpace(parts[1])
			theMessage := strings.TrimSpace(parts[2])
			if targetNick == "" || theMessage == "" {
				bot.Privmsg(channel, "Usage: ;tell <username> <message>")
				return
			}
			if err := storeTell(targetNick, userNick, theMessage); err != nil {
				log.Printf("[ERROR] storeTell: %v", err)
				bot.Privmsg(channel, fmt.Sprintf("Error storing tell: %v", err))
				return
			}
			bot.Privmsg(channel,
				fmt.Sprintf("Okay, %s. I'll tell %s next time they speak.", userNick, targetNick))
			return
		}

		// 5) Points System: ;addpoint <username>, ;rmpoint <username>
		if strings.HasPrefix(cmdLower, ";addpoint") || strings.HasPrefix(cmdLower, ";rmpoint") ||
			strings.HasPrefix(cmdLower, ";ap") || strings.HasPrefix(cmdLower, ";rp") {
			parts := strings.SplitN(msg, " ", 2)
			if len(parts) < 2 {
				bot.Privmsg(channel, "Usage: ;addpoint <username>")
				return
			}
			target := strings.TrimSpace(parts[1])
			if target == "" {
				bot.Privmsg(channel, "Usage: ;addpoint <username>")
				return
			}

			if strings.HasPrefix(cmdLower, ";addpoint") ||
				strings.HasPrefix(cmdLower, ";ap") {
				newVal, err := addPoint(userNick, target)
				if err != nil {
					log.Printf("[ERROR] addPoint: %v", err)
					bot.Privmsg(channel, fmt.Sprintf("Database error adding point: %v", err))
					return
				}
				bot.Privmsg(channel,
					fmt.Sprintf("%s: You now have %d points for %s.", userNick, newVal, target))
			} else {
				newVal, err := removePoint(userNick, target)
				if err != nil {
					log.Printf("[ERROR] removePoint: %v", err)
					bot.Privmsg(channel, fmt.Sprintf("Database error removing point: %v", err))
					return
				}
				bot.Privmsg(channel,
					fmt.Sprintf("You now have %d points for %s.", newVal, target))
			}
			return
		}

		// 6) Badge Commands
		cmd, parseErr := parseBadgeCommand(msg)
		if parseErr != nil {
			// not a badge command
			bot.Privmsg(channel, fmt.Sprintf("%s: not a badge command", userNick))
			return
		}
		switch cmd.action {
		case "add":
			storeDate := parseOrConvertDate(cmd.date)
			if _, dbErr := db.Exec(`
				INSERT INTO badges (name, date, nick) VALUES (?, ?, ?)
			`, cmd.name, storeDate, userNick); dbErr != nil {
				if strings.Contains(dbErr.Error(), "UNIQUE constraint failed") {
					bot.Privmsg(channel,
						fmt.Sprintf("%s, you already have a badge named '%s'.", userNick, cmd.name))
				} else {
					log.Printf("[ERROR] Insert badge: %v", dbErr)
					bot.Privmsg(channel, fmt.Sprintf("Failed to add badge: %v", dbErr))
				}
			} else {
				bot.Privmsg(channel,
					fmt.Sprintf("User %s added badge '%s'.", userNick, cmd.name))
			}

		case "delete":
			res, dbErr := db.Exec(`
				DELETE FROM badges WHERE name = ? AND nick = ?
			`, cmd.name, userNick)
			if dbErr != nil {
				log.Printf("[ERROR] Delete badge: %v", dbErr)
				bot.Privmsg(channel, fmt.Sprintf("Failed to delete badge: %v", dbErr))
				return
			}
			affected, _ := res.RowsAffected()
			if affected == 0 {
				bot.Privmsg(channel,
					fmt.Sprintf("No badge named '%s' found under your nickname, %s.", cmd.name, userNick))
			} else {
				bot.Privmsg(channel,
					fmt.Sprintf("User %s deleted their badge '%s'.", userNick, cmd.name))
			}

		case "show":
			rows, queryErr := db.Query(`
				SELECT name, date FROM badges WHERE nick = ?
			`, userNick)
			if queryErr != nil {
				log.Printf("[ERROR] Query badges: %v", queryErr)
				bot.Privmsg(channel, fmt.Sprintf("Failed to list badges: %v", queryErr))
				return
			}
			defer rows.Close()

			var badges []string
			for rows.Next() {
				var badgeName, storedDate string
				if err := rows.Scan(&badgeName, &storedDate); err != nil {
					log.Printf("[ERROR] Read badge row: %v", err)
					continue
				}
				daysOld := daysSince(storedDate)
				badges = append(badges, fmt.Sprintf("%s (%d days)", badgeName, daysOld))
			}
			if len(badges) == 0 {
				bot.Privmsg(channel,
					fmt.Sprintf("User %s has no badges.", userNick))
			} else {
				bot.Privmsg(channel,
					fmt.Sprintf("User %s's badges: %s", userNick, strings.Join(badges, ", ")))
			}
		}
	})

	// 8) Main loop
	log.Println("[DEBUG] Starting IRC event loop.")
	bot.Loop()
}
