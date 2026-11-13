package panel

// trafficseries.go — hourly Iran/foreign download+upload series for the
// dashboard chart (review round 3: the panel host's own live traffic is
// irrelevant — the operator wants AGENT-server traffic split by side).
// Buckets come from the traffic_hourly rollup: per (tunnel, agent, hour) the
// within-hour delta (last - first) is the honest hourly volume, grouped by
// the reporting agent's role (iran | foreign).

import (
	"net/http"
	"sort"
	"strconv"
	"time"
)

func handleUITrafficSeries(w http.ResponseWriter, r *http.Request) {
	h, ok := uiOpen(w, r)
	if !ok {
		return
	}
	hours := 24
	switch r.URL.Query().Get("range") {
	case "7d":
		hours = 168
	case "30d":
		hours = 720
	default:
		hours = 24
	}
	nowHour := time.Now().Unix() / 3600
	lo := nowHour - int64(hours)

	// A negative within-hour delta means the agent's counter RESET during
	// that hour — traffic volume can never be negative, so each row is
	// clamped to zero before summing (the reset itself contributes nothing).
	rows, err := h.Store.DB().Query(`SELECT h.hour, COALESCE(a.role,''),
		SUM(MAX(h.last_rx - h.first_rx, 0)), SUM(MAX(h.last_tx - h.first_tx, 0))
		FROM traffic_hourly h LEFT JOIN agents a ON a.id = h.agent_id
		WHERE h.hour >= ? AND h.hour < ?
		GROUP BY h.hour, COALESCE(a.role,'')`, lo, nowHour)
	if err != nil {
		uiErrWrite(w, r, "E-STORE", map[string]any{"op": "traffic series"}, err.Error())
		return
	}
	defer rows.Close()

	type pt struct {
		T          int64 `json:"t"`
		IranDL     int64 `json:"iran_dl"`
		IranUL     int64 `json:"iran_ul"`
		ForeignDL  int64 `json:"foreign_dl"`
		ForeignUL  int64 `json:"foreign_ul"`
	}
	byHour := map[int64]*pt{}
	var order []int64
	for rows.Next() {
		var hour, rx, tx int64
		var role string
		if err := rows.Scan(&hour, &role, &rx, &tx); err != nil {
			uiErrWrite(w, r, "E-STORE", map[string]any{"op": "traffic series scan"}, err.Error())
			return
		}
		p := byHour[hour]
		if p == nil {
			p = &pt{T: hour * 3600}
			byHour[hour] = p
			order = append(order, hour)
		}
		switch role {
		case "iran":
			p.IranDL += rx
			p.IranUL += tx
		case "foreign":
			p.ForeignDL += rx
			p.ForeignUL += tx
		}
	}
	if err := rows.Err(); err != nil {
		uiErrWrite(w, r, "E-STORE", map[string]any{"op": "traffic series rows"}, err.Error())
		return
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	points := make([]pt, 0, len(order))
	for _, hour := range order {
		points = append(points, *byHour[hour])
	}
	writeJSON(w, map[string]any{
		"range":  strconv.Itoa(hours) + "h",
		"points": points,
	})
}
