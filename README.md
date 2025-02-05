# shalerocksbot-go

## COMMANDS SUPPORTED

```
;addpoint <username>

;ap <username>

;rmpoint <username>

;rp <username>

;ask this or that

;badge -add -name="your_badge" -date="today"

;badge -add -name="your_badge" -date="39 days ago"

;badge -delete -name="your_badge"

;badge

;tell <username> <message>

;weather <place>

(duck hunt)

;bef

;bang

;huntscore
```

## RUNNING
```
export CHANNEL="#yourchannel"     # irc channel
export NICKNAME="yourbot"         # bot's nickname
export NICKSERV_PASS="OPTIONAL"   # nickserv password
export OWM_API_KEY="YOUR-OWM-KEY" # open weather map
go run main.go
```

# SECURITY

This code has been scanned by gosec and reviewed by the author for vulnerabilities.

