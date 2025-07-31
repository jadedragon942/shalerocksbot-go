package main

import (
	"database/sql"
	"fmt"
	"log"
	"math/rand"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
	irc "github.com/thoj/go-ircevent"
)

// Debug mode
const debug = false
const version = "0.04"

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
 * 3) Herald System Functions
 *********************************************************************/
/*********************************************************************
 * 3) Herald System Functions (Updated)
 *********************************************************************/

type heraldCommand struct {
	action     string // "add", "delete", "list", or "show"
	id         int    // for delete command
	message    string // for add command
	targetNick string // for -nick parameter (optional)
}

func parseHeraldCommand(message string) (*heraldCommand, error) {
	if !strings.HasPrefix(message, ";herald") {
		return nil, fmt.Errorf("not a ;herald command")
	}

	// ;herald -add "message here" [-nick username]
	if strings.Contains(message, "-add") {
		msgRegex := regexp.MustCompile(`-add\s+"([^"]+)"`)
		msgMatch := msgRegex.FindStringSubmatch(message)
		if len(msgMatch) < 2 {
			return nil, fmt.Errorf("missing message for add (use quotes)")
		}

		// Check for optional -nick parameter
		nickRegex := regexp.MustCompile(`-nick\s+(\S+)`)
		nickMatch := nickRegex.FindStringSubmatch(message)

		var targetNick string
		if len(nickMatch) >= 2 {
			targetNick = nickMatch[1]
		}

		return &heraldCommand{
			action:     "add",
			message:    msgMatch[1],
			targetNick: targetNick,
		}, nil
	}

	// ;herald -delete <id> [-nick username]
	if strings.Contains(message, "-delete") {
		deleteRegex := regexp.MustCompile(`-delete\s+(\d+)`)
		deleteMatch := deleteRegex.FindStringSubmatch(message)
		if len(deleteMatch) < 2 {
			return nil, fmt.Errorf("missing ID for delete")
		}
		id, err := strconv.Atoi(deleteMatch[1])
		if err != nil {
			return nil, fmt.Errorf("invalid ID for delete")
		}

		// Check for optional -nick parameter
		nickRegex := regexp.MustCompile(`-nick\s+(\S+)`)
		nickMatch := nickRegex.FindStringSubmatch(message)

		var targetNick string
		if len(nickMatch) >= 2 {
			targetNick = nickMatch[1]
		}

		return &heraldCommand{
			action:     "delete",
			id:         id,
			targetNick: targetNick,
		}, nil
	}

	// ;herald -list [-nick username] (or just ;herald)
	nickRegex := regexp.MustCompile(`-nick\s+(\S+)`)
	nickMatch := nickRegex.FindStringSubmatch(message)

	var targetNick string
	if len(nickMatch) >= 2 {
		targetNick = nickMatch[1]
	}

	return &heraldCommand{
		action:     "list",
		targetNick: targetNick,
	}, nil
}

func addHerald(nick, message string) (int, error) {
	result, err := db.Exec(`
		INSERT INTO heralds (nick, message, created_date)
		VALUES (?, ?, datetime('now'))
	`, nick, message)
	if err != nil {
		return 0, err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}

	return int(id), nil
}

func deleteHerald(nick string, id int) (bool, error) {
	result, err := db.Exec(`
		DELETE FROM heralds 
		WHERE id = ? AND nick = ?
	`, id, nick)
	if err != nil {
		return false, err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}

	return affected > 0, nil
}

func listHeralds(nick string) ([]string, error) {
	rows, err := db.Query(`
		SELECT id, message 
		FROM heralds 
		WHERE nick = ? 
		ORDER BY id
	`, nick)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var heralds []string
	for rows.Next() {
		var id int
		var message string
		if err := rows.Scan(&id, &message); err != nil {
			continue
		}
		heralds = append(heralds, fmt.Sprintf("#%d: %s", id, message))
	}

	return heralds, nil
}

func getRandomHerald(nick string) (string, error) {
	rows, err := db.Query(`
		SELECT message 
		FROM heralds 
		WHERE nick = ?
	`, nick)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var messages []string
	for rows.Next() {
		var message string
		if err := rows.Scan(&message); err != nil {
			continue
		}
		messages = append(messages, message)
	}

	if len(messages) == 0 {
		return "", nil
	}

	// Return a random herald message
	return messages[rand.Intn(len(messages))], nil
}

/*********************************************************************
 * 4) Animal Hunt Logic
 *********************************************************************/
const brown = "\x0305"
const normal = "\x0f"
const bold = "\x02"
const pink = "\x0313"

var possibleAnimals = []struct {
	name  string
	sound string
}{
	{"duck", brown + "(o)<  ・゜゜・。。・゜゜HONK" + normal},
	{"pig", brown + "~~(_ _)^" + pink + ":" + brown + " OINK" + normal},
	{"seal", bold + "(ᵔᴥᵔ) BARK" + normal},
	{"mouse", brown + "<:3)~ SQEEK" + normal},
	{"shark", bold + "____/\\_______\\o/___ AHHHH! SHARK" + normal},
}

func scheduleNextAnimal() {
	go func() {
		delay := rand.Intn(3180) + 360 // 30..300 minutes in seconds, or 6..53 minutes?
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
	bot.Privmsg(channel, chosen.sound)
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

	// 5) Create heralds table
	createHeraldsTableSQL := `
	CREATE TABLE IF NOT EXISTS heralds (
	    id           INTEGER PRIMARY KEY AUTOINCREMENT,
	    nick         TEXT NOT NULL,
	    message      TEXT NOT NULL,
	    created_date TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	`
	log.Println("[DEBUG] Ensuring heralds table exists.")
	if _, err := db.Exec(createHeraldsTableSQL); err != nil {
		log.Fatalf("[FATAL] Failed to create heralds table: %v", err)
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
	bot.Debug = false
	bot.VerboseCallbackHandler = false

	log.Println("[DEBUG] Attempting to connect to IRC server...")
	if err := bot.Connect(bot.Server); err != nil {
		log.Fatalf("[FATAL] Failed to connect to IRC server: %v", err)
	}

	// IRC Callbacks

	/*
		bot.AddCallback("*", func(e *irc.Event) {
			log.Printf("[IRC EVENT] Code: %s | Source: %s | Args: %v | Raw: %s",
				e.Code, e.Source, e.Arguments, e.Raw)
		})
	*/

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

	// Handle JOIN events for herald messages
	bot.AddCallback("JOIN", func(e *irc.Event) {
		joiningNick := e.Nick

		// Don't herald the bot itself
		if joiningNick == bot.GetNick() {
			return
		}

		// Get a random herald message for this user
		heraldMsg, err := getRandomHerald(joiningNick)
		if err != nil {
			log.Printf("[ERROR] getRandomHerald: %v", err)
			return
		}

		// If they have a herald message, display it
		if heraldMsg != "" {
			bot.Privmsg(channel, heraldMsg)
		}
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

			// Record in DB
			if err := recordAnimalHunt(userNick, theAnimal, action); err != nil {
				log.Printf("[ERROR] recordAnimalHunt failed: %v", err)
				bot.Privmsg(channel, fmt.Sprintf("Database error: %v", err))
				return
			}

			// SPECIAL REVERSE LOGIC FOR SHARKS
			if theAnimal == "shark" {
				if action == "shoot" {
					// User shot the shark, save a swimmer +1 point
					newVal, _ := addPoint(userNick, userNick)
					bot.Privmsg(channel,
						fmt.Sprintf("You shot a shark and saved a swimmer! Points: %d", newVal))
				} else {
					// User befriended the shark, the swimmer dies -1 point
					newVal, _ := removePoint(userNick, userNick)
					bot.Privmsg(channel,
						fmt.Sprintf("You befriend the shark but the swimmer gets it! Points: %d", newVal))
				}
				return
			}

			// NORMAL LOGIC FOR NON-SHARKS
			befCount, shotCount, _ := getHuntStats(userNick)
			totalPoints := befCount + shotCount
			if action == "befriend" {
				bot.Privmsg(channel,
					fmt.Sprintf("%s befriended the %s! Total points: %d (befriended: %d, shot: %d)",
						userNick, theAnimal, totalPoints, befCount, shotCount))
			} else {
				bot.Privmsg(channel,
					fmt.Sprintf("%s shot the %s! Total points: %d (befriended: %d, shot: %d)",
						userNick, theAnimal, totalPoints, befCount, shotCount))
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
			totalPoints := befCount + shotCount
			bot.Privmsg(channel,
				fmt.Sprintf("%s's total hunt points: %d (befriended: %d, shot: %d)",
					userNick, totalPoints, befCount, shotCount))
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
				bot.Privmsg(channel, "Usage: ;addpoint <username> OR ;rmpoint <username>")
				return
			}
			target := strings.TrimSpace(parts[1])
			if target == "" {
				bot.Privmsg(channel, "Usage: ;addpoint <username> OR ;rmpoint <username>")
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

		// 6) Herald Commands (Updated section)
		heraldCmd, heraldErr := parseHeraldCommand(msg)
		if heraldErr == nil {
			switch heraldCmd.action {
			case "add":
				// Determine which nick to add the herald for
				targetNick := userNick // Default to the user who sent the command
				if heraldCmd.targetNick != "" {
					targetNick = heraldCmd.targetNick
				}

				id, err := addHerald(targetNick, heraldCmd.message)
				if err != nil {
					log.Printf("[ERROR] addHerald: %v", err)
					bot.Privmsg(channel, fmt.Sprintf("Failed to add herald: %v", err))
					return
				}

				if heraldCmd.targetNick != "" {
					bot.Privmsg(channel,
						fmt.Sprintf("%s added herald #%d for %s: %s", userNick, id, targetNick, heraldCmd.message))
				} else {
					bot.Privmsg(channel,
						fmt.Sprintf("%s added herald #%d: %s", userNick, id, heraldCmd.message))
				}

			case "delete":
				// Determine which nick to delete the herald from
				targetNick := userNick // Default to the user who sent the command
				if heraldCmd.targetNick != "" {
					targetNick = heraldCmd.targetNick
				}

				deleted, err := deleteHerald(targetNick, heraldCmd.id)
				if err != nil {
					log.Printf("[ERROR] deleteHerald: %v", err)
					bot.Privmsg(channel, fmt.Sprintf("Failed to delete herald: %v", err))
					return
				}

				if deleted {
					if heraldCmd.targetNick != "" {
						bot.Privmsg(channel,
							fmt.Sprintf("%s deleted herald #%d for %s.", userNick, heraldCmd.id, targetNick))
					} else {
						bot.Privmsg(channel,
							fmt.Sprintf("%s deleted herald #%d.", userNick, heraldCmd.id))
					}
				} else {
					if heraldCmd.targetNick != "" {
						bot.Privmsg(channel,
							fmt.Sprintf("Herald #%d not found for %s.", heraldCmd.id, targetNick))
					} else {
						bot.Privmsg(channel,
							fmt.Sprintf("Herald #%d not found for %s.", heraldCmd.id, userNick))
					}
				}

			case "list":
				// Determine which nick to list heralds for
				targetNick := userNick // Default to the user who sent the command
				if heraldCmd.targetNick != "" {
					targetNick = heraldCmd.targetNick
				}

				heralds, err := listHeralds(targetNick)
				if err != nil {
					log.Printf("[ERROR] listHeralds: %v", err)
					bot.Privmsg(channel, fmt.Sprintf("Failed to list heralds: %v", err))
					return
				}

				if len(heralds) == 0 {
					bot.Privmsg(channel, fmt.Sprintf("%s has no heralds.", targetNick))
				} else {
					bot.Privmsg(channel,
						fmt.Sprintf("%s's heralds: %s", targetNick, strings.Join(heralds, " | ")))
				}
			}
			return
		}

		// 7) Reset Badge Command
		if strings.HasPrefix(strings.ToLower(msg), ";resetbadge") {
			targetNick := e.Nick

			// Reset the Sobriety badge to today's date
			res, dbErr := db.Exec(`
				UPDATE badges SET date = datetime('now') WHERE name = 'Sobriety' AND nick = ?
			`, targetNick)
			if dbErr != nil {
				log.Printf("[ERROR] Reset badge: %v", dbErr)
				bot.Privmsg(channel, fmt.Sprintf("Failed to reset badge: %v", dbErr))
				return
			}
			affected, _ := res.RowsAffected()
			if affected == 0 {
				bot.Privmsg(channel,
					fmt.Sprintf("No Sobriety badge found for %s.", targetNick))
			} else {
				bot.Privmsg(channel,
					fmt.Sprintf("%s reset %s's Sobriety badge to day 1.", userNick, targetNick))
			}
			return
		}

		// 8) Badge Commands
		cmd, parseErr := parseBadgeCommand(msg)
		if parseErr != nil {
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

	// 9) Main loop
	log.Println("[DEBUG] Starting IRC event loop.")
	bot.Loop()
}
