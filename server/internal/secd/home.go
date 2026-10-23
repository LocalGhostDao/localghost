package secd

// HOME ON THE WAY BACK. The phone asks for its notifications every quarter hour and sends its
// trail while the trail is on; both answers carry home's numbers as they stand (hw.HomeSnap), so
// the phone keeps them and home, CRYPTO and the lock screen open on the latest without a fetch of
// their own. Read from the Redis copies tallyd and synthd keep; a miss reads Postgres once and puts
// the copy back. Nothing is said when neither answers: the field is left out.

import (
	"net/http"
	"strconv"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/apparedis"
	"github.com/LocalGhostDao/localghost/server/internal/hw"
)

// homeSnap is home's numbers for the mounted volume; ok is false when nothing could be read.
func (s *Server) homeSnap(mounted int) (hw.HomeSnap, bool) {
	if s.notif == nil || mounted < 0 {
		return hw.HomeSnap{}, false
	}
	now := time.Now()
	rd, rerr := s.notif.Cache(mounted)
	var rates *hw.RatesDoc
	var news *hw.NewsDoc
	var fast *hw.Fast
	if rerr == nil {
		var d hw.RatesDoc
		if hw.HotGet(rd, hw.HotRates, &d) {
			rates = &d
		}
		var n hw.NewsDoc
		if hw.HotGet(rd, hw.HotNews, &n) {
			news = &n
		}
		var f hw.Fast
		if hw.HotGet(rd, hw.HotFast, &f) {
			fast = &f
		}
	}
	if rates == nil || news == nil {
		db, err := s.notif.DB(mounted)
		if err == nil && rates == nil {
			if d, err := hw.RatesDocNow(db, now); err == nil {
				rates = &d
				if rerr == nil {
					_ = hw.HotPut(rd, hw.HotRates, d, time.Minute)
				}
			}
		}
		if err == nil && news == nil {
			from := now.Add(-hw.NewsHotDays * 24 * time.Hour).Unix()
			if n, err := hw.NewsDocNow(db, from, 0, now.Unix()); err == nil {
				news = &n
				if rerr == nil {
					_ = hw.HotPut(rd, hw.HotNews, n, time.Minute)
				}
			}
		}
	}
	if rates == nil && news == nil {
		return hw.HomeSnap{}, false
	}
	snap := hw.MakeHomeSnap(rates, fast, news, now)
	if db, err := s.notif.DB(mounted); err == nil {
		snap.ForYou = forYouFor(db, rd, rerr == nil, news, now)
		if evs, err := hw.CalendarUpcoming(db, now, 36, 3); err == nil && len(evs) > 0 {
			for i := range evs {
				evs[i].Notes = "" // HOME shows the time, title and place; the rest is the DAY page's
			}
			snap.Calendar = evs
		}
	}
	return snap, true
}

// forYouFor is the FOR YOU card: Redis's copy while it stands (young, and the trail not moved a
// kilometre from where its places were measured), else made again and kept.
func forYouFor(db hw.Querier, rd *apparedis.ReadWrite, hot bool, news *hw.NewsDoc, now time.Time) *hw.ForYou {
	ts, lat, lon := hw.TrailNewest(db)
	if hot {
		if raw, ok, err := rd.Get(hw.HotForYou); err == nil && ok {
			if f, ok := hw.UnmarshalForYouCopy([]byte(raw)); ok && f.StillFresh(now, ts, lat, lon) {
				return &f
			}
		}
	}
	f := hw.ForYouNow(db, news, now)
	if hot {
		if b, err := f.MarshalCopy(); err == nil {
			_, _ = rd.Do("SET", hw.HotForYou, string(b), "EX", strconv.Itoa(int(hw.ForYouFresh.Seconds())))
		}
	}
	return &f
}

// handleHome , GET /v1/home , home's numbers, the brief and FOR YOU as they stand: what the
// notifications poll and the trail's upload carry, asked for when home is pulled to refresh.
func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	snap, ok := s.homeSnap(mounted)
	if !ok {
		s.appearsDown(w)
		return
	}
	writeJSON(w, snap)
}
