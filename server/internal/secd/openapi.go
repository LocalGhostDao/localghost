package secd

import (
	"encoding/json"
	"net/http"
	"reflect"
	"sort"
	"strings"

	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/monitor"
)

// OpenAPI generation, from scratch (no swaggo, no oapi-codegen , the spec for a dozen routes does
// not justify a dependency). The design goal is that the spec CANNOT silently drift from the code:
//
//  1. The route table below is the single source of truth. Handler() builds the mux FROM it, so a
//     route cannot exist in the server without existing in the spec, or vice versa , that guarantee
//     is structural, not a convention.
//  2. Request/response shapes are documented as Go types (below) and turned into schemas by
//     reflection over their json tags. Where a handler still builds ad-hoc maps, the doc type is
//     the contract and openapi_test.go calls the LIVE handler and unmarshals its output into the
//     doc type with DisallowUnknownFields , if the handler grows or renames a field, the build
//     fails until the doc type follows. Shapes that cannot be exercised in a unit test are marked
//     loose (additionalProperties) rather than guessed at; a wrong spec is worse than a vague one.
//
// The spec is served at /v1/openapi.json , behind the same edge as everything else (device cert or
// uniform 503), so it documents the API to enrolled devices without describing the box to anyone
// else.

// route is one entry of the single source of truth.
type route struct {
	Method   string // primary method for the spec; handlers still enforce their own
	Path     string
	Summary  string
	Auth     bool // requires the bearer session token from a PIN unlock
	Request  any  // zero value of the request doc type; nil = no body
	Response any  // zero value of the response doc type; nil = binary/stream
	Binary   bool // response is raw bytes (model download)
	Handler  http.HandlerFunc
}

// --- documented wire shapes -------------------------------------------------------------------
// These document what the handlers actually emit. The ones exercised by openapi_test.go are hard
// contracts (DisallowUnknownFields against live output); the rest follow the same rule as soon as
// their backing stores are testable.

type healthDoc struct {
	OK      bool   `json:"ok"`
	Service string `json:"service"`
}

type statusDoc struct {
	Mounted  bool            `json:"mounted"`
	Services []ServiceStatus `json:"services"`
}

type infoDoc struct {
	Locked      bool `json:"locked"`
	MountedSlot *int `json:"mountedSlot,omitempty"`
	Daemons     *int `json:"daemons,omitempty"`
}

type stageDoc struct {
	Stage string `json:"stage"`
	State string `json:"state"`
}

type lockDoc struct {
	Locked bool       `json:"locked"`
	Steps  []stageDoc `json:"steps"`
	Error  string     `json:"error,omitempty"`
}

type unlockStartDoc struct {
	Started bool `json:"started"`
	// Run names this unlock; the poll presents it (?run=) to collect the session token, once.
	Run string `json:"run,omitempty"`
}

type unlockPollDoc struct {
	Stages []stageDoc `json:"stages"`
	Done   bool       `json:"done"`
	Failed string     `json:"failed,omitempty"`
	Token  string     `json:"token,omitempty"` // issued once, on a successful unlock
}

type okDoc struct {
	OK bool `json:"ok"`
}

type muteStatusDoc struct {
	Mutes map[string]int64 `json:"mutes"` // scope -> muted-until unix seconds
}

type muteRequestDoc struct {
	Scope   string `json:"scope"`
	Preset  string `json:"preset,omitempty"`
	Minutes int    `json:"minutes,omitempty"`
	Hours   int    `json:"hours,omitempty"`
	Days    int    `json:"days,omitempty"`
	Clear   bool   `json:"clear,omitempty"`
}

type idRequestDoc struct {
	ID int64 `json:"id"`
}

type answerRequestDoc struct {
	ID     int64  `json:"id"`
	Answer string `json:"answer"`
}

// looseList documents endpoints whose element shape is owned by the in-volume notification store
// and is not yet exercisable in a unit test. Stated plainly rather than guessed: the schema says
// "array of objects" until the store settles and the shape can be pinned like the others.
type looseList struct {
	Available     *bool            `json:"available,omitempty"`
	Notifications []map[string]any `json:"notifications"`
}

// pollDoc is the notifications poll: the batch, and home as it stands (home.go, /v1/home).
type pollDoc struct {
	Available     *bool            `json:"available,omitempty"`
	Notifications []map[string]any `json:"notifications"`
	Home          *hw.HomeSnap     `json:"home,omitempty"`
}

type modelDoc struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Detail    string `json:"detail"`
	SizeBytes int64  `json:"sizeBytes"`
	SHA256    string `json:"sha256"`
}

type trailAnswerDoc struct {
	From int64   `json:"from"`
	To   int64   `json:"to"`
	TS   []int64 `json:"ts"`
	Keep bool    `json:"keep"`
}

type trailAnsweredDoc struct {
	OK      bool `json:"ok"`
	Deleted int  `json:"deleted"`
}

type dayDoc struct {
	Day       hw.DayRow `json:"day"`
	CheckedIn bool      `json:"checkedIn"`
	Built     bool      `json:"built"`
}

type wikiHitDoc struct {
	Idx    uint32 `json:"idx"`
	Title  string `json:"title"`
	Lead   string `json:"lead"`
	Disamb bool   `json:"disamb,omitempty"`
	How    string `json:"how"`
}

type wikiArticleDoc struct {
	Idx    uint32 `json:"idx"`
	Title  string `json:"title"`
	Lead   string `json:"lead"`
	Body   string `json:"body"`
	Disamb bool   `json:"disamb"`
}

type wikiDoc struct {
	State     string         `json:"state"` // ready | importing | downloading | missing | failed | locked
	Edition   string         `json:"edition"`
	Articles  int64          `json:"articles"`
	Redirects int64          `json:"redirects"`
	Imported  int64          `json:"imported"` // entries of the file read so far
	Entries   int64          `json:"entries"`  // entries in the file
	File      string         `json:"file"`
	Answers   int            `json:"answers"`
	Error     string         `json:"error,omitempty"`
	Found     bool           `json:"found,omitempty"`
	Hits      []wikiHitDoc   `json:"hits,omitempty"`
	Article   wikiArticleDoc `json:"article,omitempty"`
}

type trailForgetDoc struct {
	TS      int64   `json:"ts"`
	RadiusM float64 `json:"radiusM,omitempty"`
	Dry     bool    `json:"dry,omitempty"`
}

type trailForgottenDoc struct {
	OK      bool    `json:"ok"`
	TS      []int64 `json:"ts"`
	Deleted int     `json:"deleted"`
}

type updateDoc struct {
	Version string `json:"version"`
	Name    string `json:"name,omitempty"`
	Commit  string `json:"commit,omitempty"`
	BuiltAt string `json:"builtAt,omitempty"`
	Go      string `json:"go"`
	Trial   struct {
		Version string `json:"version"`
		Prev    string `json:"prev"`
		State   string `json:"state"`
		Reason  string `json:"reason,omitempty"`
	} `json:"trial"`
	// Shelf is the releases the box keeps, newest taken first; one with set:true can go back on
	// (POST /v1/update/switch).
	Shelf []struct {
		Version string `json:"version"`
		Name    string `json:"name,omitempty"`
		Commit  string `json:"commit,omitempty"`
		Date    string `json:"date,omitempty"`
		Go      string `json:"go,omitempty"`
		Changes int    `json:"changes"`
		At      int64  `json:"at"`
		Set     bool   `json:"set"`
	} `json:"shelf"`
}

type sourcesDoc struct {
	Sources []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		State  string `json:"state"`
		Line   string `json:"line"`
		Detail string `json:"detail,omitempty"`
		Action string `json:"action,omitempty"`
		Label  string `json:"label,omitempty"`
		Open   string `json:"open,omitempty"`
		Bytes  int64  `json:"bytes,omitempty"`
	} `json:"sources"`
	Job *struct {
		Step      string `json:"step"`
		Region    string `json:"region,omitempty"`
		StartedAt int64  `json:"startedAt"`
		EndedAt   int64  `json:"endedAt,omitempty"`
		Running   bool   `json:"running"`
		Exit      int    `json:"exit"`
		Last      string `json:"last"`
	} `json:"job"`
}

type sourcesFetchDoc struct {
	Step   string `json:"step"`
	Region string `json:"region,omitempty"`
}

type updateSwitchDoc struct {
	Version string `json:"version"`
}

type updateAppliedDoc struct {
	OK      bool     `json:"ok"`
	Version string   `json:"version,omitempty"`
	Why     string   `json:"why,omitempty"`
	Changes []string `json:"changes,omitempty"`
}

type rekeyRequestDoc struct {
	SPKI string `json:"spki"`
	Sig  string `json:"sig"`
}

type rekeyDoc struct {
	OK   bool   `json:"ok"`
	Cert string `json:"cert"`
}

type trailKeyDoc struct {
	Have    bool   `json:"have"`
	Public  string `json:"public,omitempty"`
	Private string `json:"private,omitempty"`
}

type modelsDoc struct {
	Models []modelDoc `json:"models"`
}

// routes is the single source of truth: mux registration and the OpenAPI document both derive from
// this table and nothing else.
func (s *Server) routes() []route {
	return []route{
		{Method: "GET", Path: "/v1/health", Summary: "Cheap reachability check; no account, no session.",
			Response: healthDoc{}, Handler: s.handleHealth},
		{Method: "POST", Path: "/v1/unlock", Summary: "Start a PIN unlock; progress is read at /v1/unlock/poll.",
			Request: unlockRequest{}, Response: unlockStartDoc{}, Handler: s.handleUnlockStart},
		{Method: "GET", Path: "/v1/unlock/poll", Summary: "Unlock progress; on success carries the fresh session token.",
			Response: unlockPollDoc{}, Handler: s.handleUnlockPoll},
		{Method: "POST", Path: "/v1/lock", Summary: "Spin the box down: stop DBs, unmount, close LUKS, revoke the session.",
			Auth: true, Response: lockDoc{}, Handler: s.handleLock},
		{Method: "GET", Path: "/v1/info", Summary: "Box + mounted-account summary for the home screen.",
			Response: infoDoc{}, Handler: s.handleInfo},
		{Method: "GET", Path: "/v1/status", Summary: "Per-service supervisor status for the Ghost Status screen.",
			Auth: true, Response: statusDoc{}, Handler: s.handleStatus},
		{Method: "GET", Path: "/v1/notifications", Summary: "Poll pending notifications for the mounted account.",
			Auth: true, Response: pollDoc{}, Handler: s.handleNotifications},
		{Method: "GET", Path: "/v1/notifications/mute", Summary: "Current notification mutes per scope.",
			Auth: true, Response: muteStatusDoc{}, Handler: s.handleMute},
		{Method: "POST", Path: "/v1/notifications/mute", Summary: "Set or clear a notification mute for a scope.",
			Auth: true, Request: muteRequestDoc{}, Response: okDoc{}, Handler: s.handleMute},
		{Method: "GET", Path: "/v1/notifications/list", Summary: "Full notification history for the mounted account.",
			Auth: true, Response: looseList{}, Handler: s.handleNotificationList},
		{Method: "POST", Path: "/v1/notifications/seen", Summary: "Mark a notification seen.",
			Auth: true, Request: idRequestDoc{}, Response: okDoc{}, Handler: s.handleNotificationSeen},
		{Method: "POST", Path: "/v1/notifications/delete", Summary: "Delete a notification.",
			Auth: true, Request: idRequestDoc{}, Response: okDoc{}, Handler: s.handleNotificationDelete},
		{Method: "POST", Path: "/v1/notifications/answer", Summary: "Answer a notification's question.",
			Auth: true, Request: answerRequestDoc{}, Response: okDoc{}, Handler: s.handleNotificationAnswer},
		{Method: "GET", Path: "/v1/model", Summary: "The box model: ready, or loading (phase, percent, time left). It loads after the unlock.",
			Auth: true, Response: modelState{}, Handler: s.handleModel},
		{Method: "POST", Path: "/v1/geo/trail/answer", Summary: "Were you there? keep=true remembers the stretch; keep=false deletes its points for good.",
			Auth: true, Request: trailAnswerDoc{}, Response: trailAnsweredDoc{}, Handler: s.handleTrailAnswer},
		{Method: "POST", Path: "/v1/phone/net", Summary: "The phone's network ({net: wifi|mobile|none}), every quarter hour: on Wi-Fi the phone fetches the feeds and tickers for the box, otherwise the box fetches for itself.",
			Auth: true, Request: looseList{}, Response: okDoc{}, Handler: s.handlePhoneNet},
		{Method: "GET", Path: "/v1/fetch/list", Summary: "What the phone fetches for the box: the news feeds (the box's list) and the market tickers, with how often.",
			Auth: true, Response: fetchListDoc{}, Handler: s.handleFetchList},
		{Method: "POST", Path: "/v1/news/fetched", Summary: "The feeds' bytes as the phone fetched them ({fetchedAt, feeds:[{id,status,error,body}]}), spooled for ghost.synthd.",
			Auth: true, Request: looseList{}, Response: okDoc{}, Handler: s.handleNewsFetched},
		{Method: "POST", Path: "/v1/rates/fetched", Summary: "The tickers' bodies as the phone fetched them ({fetchedAt, sources:[{id,status,error,body}]}), spooled for ghost.tallyd.",
			Auth: true, Request: looseList{}, Response: okDoc{}, Handler: s.handleRatesFetched},
		{Method: "GET", Path: "/v1/news", Summary: "The stories the feeds add up to (?since=<unix>&limit=N), each with its outlets' entries and the box's summary (a lead and points), and the day's brief (one point per story, briefStories in order); the last two days come from Redis.",
			Auth: true, Response: newsDoc{}, Handler: s.handleNews},
		{Method: "POST", Path: "/v1/news/brief", Summary: "The day's brief written now, whatever its age (home's button): written or why not (fewer than two summaries, the model on the CPU, an answer that did not hold to the stories), and the brief as it stands. Up to two minutes.",
			Auth: true, Response: briefNowDoc{}, Handler: s.handleNewsBrief},
		{Method: "GET", Path: "/v1/rates", Summary: "The box's market numbers: the ECB table of the newest day, the USD index per symbol and its making, the top 100 coins, and the market index (the fifty largest, weighted by last month's volume).",
			Auth: true, Response: hw.RatesDoc{}, Handler: s.handleRates},
		{Method: "GET", Path: "/v1/about", Summary: "The note about me and my people, the name it gives, and how many memories the box made from it (pending while synthd has not read this version).",
			Auth: true, Response: aboutDoc{}, Handler: s.handleAbout},
		{Method: "POST", Path: "/v1/about", Summary: "Writes the note about me and my people ({text}, 8,000 characters at most); synthd makes memories from it at its next pass: facts about me, and one per person.",
			Auth: true, Request: looseList{}, Response: aboutDoc{}, Handler: s.handleAbout},
		{Method: "GET", Path: "/v1/rates/sparks", Summary: "A week of hourly closes for every coin the box prices (oldest first), for CRYPTO's rows; from Redis for ten minutes at a time.",
			Auth: true, Response: sparksDoc{}, Handler: s.handleRatesSparks},
		{Method: "GET", Path: "/v1/coins/info", Summary: "One coin's page (?symbol=BTC): Coinbase's description, colour, site and white paper; rank, market cap, supply and 24-hour volume from the list; the box's price and how it is blended right now, market by market (price, dollars, volume, age, weight, or why left out).",
			Auth: true, Response: hw.CoinDoc{}, Handler: s.handleCoinInfo},
		{Method: "GET", Path: "/v1/home", Summary: "Home as it stands: BTC, ETH and SOL with the day's change, CRYPTO50, the brief and the day's most-told stories, and FOR YOU (places near the trail to the person's taste, this date in earlier years, the day's stories touching what they said about themselves). The notifications poll and the trail's upload carry the same as \"home\".",
			Auth: true, Response: hw.HomeSnap{}, Handler: s.handleHome},
		{Method: "GET", Path: "/v1/rates/fast", Summary: "BTC, ETH and SOL as of the last five seconds (Coinbase's last trade over the box's index), each with its 24-hour change; from Redis only.",
			Auth: true, Response: fastDoc{}, Handler: s.handleRatesFast},
		{Method: "GET", Path: "/v1/rates/series", Summary: "A symbol's or the market index's price every minute (res=1m, the last week) or every hour (res=1h, the last thirty days): ?code=BTC|CRYPTO50&res=1m|1h&hours=N, oldest first.",
			Auth: true, Response: looseList{}, Handler: s.handleRatesSeries},
		{Method: "GET", Path: "/v1/feeds/status", Summary: "How the data the box pulls in is doing, for Box Status: per section (who fetches, prices, exchanges, price history, CRYPTO50, ECB, rank list, daily candles, news) a state (ok, filling, waiting, flaky, late, failing), a line, the newest piece's age and the detail rows.",
			Auth: true, Response: monitor.Report{}, Handler: s.handleFeedsStatus},
		{Method: "GET", Path: "/v1/rates/history", Summary: "A symbol's daily USD closes (the box's daily index), a currency's daily ECB rate, or the market index (?code=BTC|GBP|CRYPTO50&days=365), newest first.",
			Auth: true, Response: looseList{}, Handler: s.handleRatesHistory},
		{Method: "GET", Path: "/v1/geo/countries", Summary: "Every country with the tiles the box holds for it (streets, main roads, coast) and their size, for the whole-country map download.",
			Auth: true, Response: countriesDoc{}, Handler: s.handleCountries},
		{Method: "GET", Path: "/v1/geo/country", Summary: "One country's tiles as index keys (?code=GR; y*cols+x on each grid), only cells the box has a tile for.",
			Auth: true, Response: countryDoc{}, Handler: s.handleCountry},
		{Method: "GET", Path: "/v1/geo/at", Summary: "The country a point is in (?lat=&lon=), from the box's own Natural Earth polygons; 404 at sea. The phone's lock-screen phrases follow it, so no geocoder outside the box ever sees a fix.",
			Auth: true, Response: whereDoc{}, Handler: s.handleAt},
		{Method: "GET", Path: "/v1/day", Summary: "One day's summary from photos, trail, health, voice notes and the check-in (?d=YYYY-MM-DD; &build=1 writes it now).",
			Auth: true, Response: dayDoc{}, Handler: s.handleDay},
		{Method: "GET", Path: "/v1/wiki", Summary: "The box's own Wikipedia, in its database once the mirror's file is imported: the state and counts; ?q= the articles a phrase names, surest first; ?idx= one article whole. Nothing leaves the box.",
			Auth: true, Response: wikiDoc{}, Handler: s.handleWiki},
		{Method: "POST", Path: "/v1/geo/trail/forget", Summary: "Delete one fix and its neighbours at the same spot (dry=true only says which).",
			Auth: true, Request: trailForgetDoc{}, Response: trailForgottenDoc{}, Handler: s.handleTrailForget},
		{Method: "GET", Path: "/v1/trail/key", Summary: "This device's trail key, to read its own sealed trail while unlocked ({have:false} when none).",
			Auth: true, Response: trailKeyDoc{}, Handler: s.handleTrailKey},
		{Method: "POST", Path: "/v1/trail/key", Summary: "The phone hands its trail key (raw X25519, base64) to the vault, once.",
			Auth: true, Request: trailKeyFile{}, Response: okDoc{}, Handler: s.handleTrailKey},
		{Method: "GET", Path: "/v1/update", Summary: "The build the box runs (version, name, commit, when, Go), a release on trial (trial, confirmed, rolled back), and the shelf of releases it keeps.",
			Auth: true, Response: updateDoc{}, Handler: s.handleUpdate},
		{Method: "POST", Path: "/v1/update/file", Summary: "One file of the signed server set (?name=MANIFEST.txt first, then its .asc, then <build>/server/<file>).",
			Auth: true, Response: okDoc{}, Handler: s.handleUpdateFile},
		{Method: "POST", Path: "/v1/update/apply", Summary: "Verify the uploaded set against the pinned site key, put it on, lock and restart onto it.",
			Auth: true, Response: updateAppliedDoc{}, Handler: s.handleUpdateApply},
		{Method: "GET", Path: "/v1/weather", Summary: "The forecast nearest ?lat=&lon= from the box's daily pull of the world's larger places (the position goes to the box and nowhere else), with the pull's state.",
			Auth: true, Response: okDoc{}, Handler: s.handleWeather},
		{Method: "GET", Path: "/v1/sources", Summary: "What the box draws on beyond the archive (Wikipedia, news, crypto, weather, maps and heights, speech), each with its state, and the fetch job running if any.",
			Auth: true, Response: sourcesDoc{}, Handler: s.handleSources},
		{Method: "POST", Path: "/v1/sources/fetch", Summary: "Start tools/update.sh <step> from the mirror (wiki, maps with an optional region, speech, engine, weights), one at a time; GET /v1/sources follows it.",
			Auth: true, Request: sourcesFetchDoc{}, Response: okDoc{}, Handler: s.handleSourcesFetch},
		{Method: "POST", Path: "/v1/update/switch", Summary: "A release from the shelf back on: its kept set verified again, unpacked, put on, lock and restart onto it (on trial like a new one).",
			Auth: true, Request: updateSwitchDoc{}, Response: updateAppliedDoc{}, Handler: s.handleUpdateSwitch},
		{Method: "POST", Path: "/v1/update/rollback", Summary: "Put the earlier build back, lock and restart onto it.",
			Auth: true, Response: updateAppliedDoc{}, Handler: s.handleUpdateRollback},
		{Method: "POST", Path: "/v1/device/rekey", Summary: "A certificate for a key the phone made itself (spki + a signature proving it holds the key).",
			Auth: true, Request: rekeyRequestDoc{}, Response: rekeyDoc{}, Handler: s.handleRekey},
		{Method: "POST", Path: "/v1/device/rekey/confirm", Summary: "Over the new certificate: the one it replaced is retired (answered as if down from then on).",
			Auth: true, Response: okDoc{}, Handler: s.handleRekeyConfirm},
		{Method: "GET", Path: "/v1/models", Summary: "The model catalogue the box offers the phone.",
			Response: modelsDoc{}, Handler: s.handleModels},
		{Method: "GET", Path: "/v1/models/", Summary: "Model bytes by id (/v1/models/{id}); a large binary download.",
			Binary: true, Handler: s.handleModelBytes},
		{Method: "GET", Path: "/v1/openapi.json", Summary: "This document.",
			Handler: s.handleOpenAPI},
	}
}

// handleOpenAPI serves the generated spec. Behind the cert-gated edge like every other route, so it
// documents the API to enrolled devices and describes nothing to anyone else.
func (s *Server) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.openAPIDocument())
}

// openAPIDocument builds an OpenAPI 3.0 document from the route table by reflection.
func (s *Server) openAPIDocument() map[string]any {
	paths := map[string]any{}
	for _, rt := range s.routes() {
		op := map[string]any{
			"summary":   rt.Summary,
			"responses": map[string]any{},
		}
		responses := op["responses"].(map[string]any)
		switch {
		case rt.Binary:
			responses["200"] = map[string]any{
				"description": "raw bytes",
				"content": map[string]any{
					"application/octet-stream": map[string]any{
						"schema": map[string]any{"type": "string", "format": "binary"},
					},
				},
			}
		case rt.Response != nil:
			responses["200"] = map[string]any{
				"description": "OK",
				"content": map[string]any{
					"application/json": map[string]any{"schema": schemaOf(reflect.TypeOf(rt.Response))},
				},
			}
		default:
			responses["200"] = map[string]any{"description": "OK"}
		}
		// The edge's appears-down behaviour, documented honestly: anything unauthenticated ,
		// missing cert at nginx, missing session here , is a generic unavailable, never a 401/403.
		responses["503"] = map[string]any{
			"description": "appears-down: unauthenticated, locked, or genuinely unavailable , deliberately indistinguishable",
		}
		if rt.Request != nil {
			op["requestBody"] = map[string]any{
				"required": true,
				"content": map[string]any{
					"application/json": map[string]any{"schema": schemaOf(reflect.TypeOf(rt.Request))},
				},
			}
		}
		if rt.Auth {
			op["security"] = []any{map[string]any{"session": []any{}}}
		}
		item, ok := paths[rt.Path].(map[string]any)
		if !ok {
			item = map[string]any{}
			paths[rt.Path] = item
		}
		item[strings.ToLower(rt.Method)] = op
	}
	return map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title":       "ghost.secd",
			"version":     "1",
			"description": "The LocalGhost box API. Transport auth is a box-issued device certificate at the nginx edge (mTLS); requests without it never reach this daemon. Session auth on top is a bearer token issued by a successful PIN unlock.",
		},
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"session": map[string]any{"type": "http", "scheme": "bearer",
					"description": "session token from /v1/unlock/poll after a correct PIN"},
				"deviceCert": map[string]any{"type": "mutualTLS",
					"description": "box-issued device certificate, delivered in the enrolment QR; enforced by nginx before this daemon"},
			},
		},
		"security": []any{map[string]any{"deviceCert": []any{}}},
		"paths":    paths,
	}
}

// schemaOf turns a Go type into a JSON schema by reflection over json tags. Deliberately covers
// only what the doc types use: structs, pointers, slices, maps, strings, bools, ints, floats.
func schemaOf(t reflect.Type) map[string]any {
	switch t.Kind() {
	case reflect.Pointer:
		return schemaOf(t.Elem()) // optionality is expressed via required-list absence
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice, reflect.Array:
		return map[string]any{"type": "array", "items": schemaOf(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": schemaOf(t.Elem())}
	case reflect.Interface:
		return map[string]any{} // any
	case reflect.Struct:
		props := map[string]any{}
		var required []string
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			tag := f.Tag.Get("json")
			if tag == "-" {
				continue
			}
			name := f.Name
			omitempty := false
			if tag != "" {
				parts := strings.Split(tag, ",")
				if parts[0] != "" {
					name = parts[0]
				}
				for _, p := range parts[1:] {
					if p == "omitempty" {
						omitempty = true
					}
				}
			}
			props[name] = schemaOf(f.Type)
			if !omitempty && f.Type.Kind() != reflect.Pointer {
				required = append(required, name)
			}
		}
		out := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			sort.Strings(required)
			out["required"] = required
		}
		return out
	default:
		return map[string]any{}
	}
}
