package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-pulse/internal/rollup"
)

// Overview is the dashboard landing payload (PL-ROLL-005/006).
type Overview struct {
	Stream         string            `json:"stream"`
	Msgs           uint64            `json:"msgs"`
	Bytes          uint64            `json:"bytes"`
	NumSubjects    uint64            `json:"num_subjects"`
	FirstTime      time.Time         `json:"first_time"`
	LastTime       time.Time         `json:"last_time"`
	Last1h         rollup.KeyCount   `json:"last_1h"`
	Last24h        rollup.KeyCount   `json:"last_24h"`
	TopTypes24h    []rollup.KeyCount `json:"top_types_24h"`
	TopPrefixes24h []rollup.KeyCount `json:"top_prefixes_24h" doc:"Depth-2 prefixes (evt.<domain>)"`
	TopSubjects7d  []rollup.KeyCount `json:"top_subjects_7d" doc:"Depth-4 prefixes (evt.<domain>.<entity>.<action>)"`
	Forecast       rollup.Forecast   `json:"forecast"`
	Index          IndexStats        `json:"index"`
}

// IndexStats describes the subject index.
type IndexStats struct {
	Indexed int   `json:"indexed"`
	Cap     int   `json:"cap"`
	Dropped int64 `json:"dropped" doc:"Subject updates not indexed because the cap was reached"`
}

// SeriesSet is a throughput chart payload.
type SeriesSet struct {
	Range  string   `json:"range"`
	Step   int64    `json:"step" doc:"Bucket size in seconds"`
	From   int64    `json:"from"`
	To     int64    `json:"to"`
	Dim    string   `json:"dim"`
	Series []Series `json:"series"`
}

// Series is one line of a chart.
type Series struct {
	Key    string         `json:"key"`
	Points []rollup.Point `json:"points"`
}

type streamQuery struct {
	Stream string `query:"stream" default:"EVENTS" pattern:"^[A-Za-z0-9_-]{1,64}$"`
}

func registerRollups(api huma.API, d *Deps) {
	huma.Register(api, huma.Operation{OperationID: "get-overview", Method: http.MethodGet, Path: "/overview", Summary: "Overview KPIs and forecast", Tags: []string{"rollups"}},
		func(ctx context.Context, in *streamQuery) (*struct{ Body Overview }, error) {
			s, err := d.JS.Stream(ctx, in.Stream)
			if err != nil {
				return nil, mapQueryErr(d, "overview", err)
			}
			info := s.CachedInfo()
			o := Overview{Stream: in.Stream, Msgs: info.State.Msgs, Bytes: info.State.Bytes, NumSubjects: info.State.NumSubjects,
				FirstTime: info.State.FirstTime, LastTime: info.State.LastTime}
			r := d.Rollup
			now := time.Now()
			f1, t1 := rollup.Ranges["1h"].Bounds(now)
			f24, t24 := rollup.Ranges["24h"].Bounds(now)
			f7, t7 := rollup.Ranges["7d"].Bounds(now)
			var errs []error
			var e error
			o.Last1h, e = r.Sum(ctx, in.Stream, rollup.Minute, rollup.DimTotal, "", f1, t1)
			errs = append(errs, e)
			o.Last24h, e = r.Sum(ctx, in.Stream, rollup.Minute, rollup.DimTotal, "", f24, t24)
			errs = append(errs, e)
			o.TopTypes24h, e = r.Top(ctx, in.Stream, rollup.Minute, rollup.DimType, f24, t24, 8)
			errs = append(errs, e)
			o.TopPrefixes24h, e = r.Top(ctx, in.Stream, rollup.Minute, rollup.PrefixDim(2), f24, t24, 10)
			errs = append(errs, e)
			d7 := rollup.Day
			o.TopSubjects7d, e = r.Top(ctx, in.Stream, d7, rollup.PrefixDim(4), f7-f7%d7.Step, t7, 10)
			errs = append(errs, e)
			avg, days, e := r.AverageDaily(ctx, in.Stream)
			errs = append(errs, e)
			o.Forecast = rollup.ComputeForecast(int64(info.State.Bytes), info.Config.MaxBytes, avg, days, now) //nolint:gosec // stream bytes fit int64
			o.Index.Indexed, o.Index.Dropped, e = r.IndexStats(ctx, in.Stream)
			errs = append(errs, e)
			o.Index.Cap = r.SubjectCap()
			if err := errors.Join(errs...); err != nil {
				return nil, d.internalErr("overview", err)
			}
			return &struct{ Body Overview }{o}, nil
		})

	huma.Register(api, huma.Operation{OperationID: "get-throughput", Method: http.MethodGet, Path: "/throughput", Summary: "Throughput time series", Tags: []string{"rollups"}},
		func(ctx context.Context, in *struct {
			Stream string `query:"stream" default:"EVENTS" pattern:"^[A-Za-z0-9_-]{1,64}$"`
			Range  string `query:"range" default:"24h" enum:"1h,24h,7d,30d,1y"`
			Dim    string `query:"dim" default:"total" enum:"total,type,p1,p2,p3,p4"`
			Top    int    `query:"top" default:"6" minimum:"1" maximum:"12" doc:"For dim != total: the N keys with most events"`
		}) (*struct{ Body SeriesSet }, error) {
			rg := rollup.Ranges[in.Range]
			from, to := rg.Bounds(time.Now())
			res := rg.Res
			depth := map[string]int{"p1": 1, "p2": 2, "p3": 3, "p4": 4}[in.Dim]
			for depth > res.MaxDepth { // coarser resolution keeps deeper prefixes
				if res == rollup.Minute {
					res = rollup.Hour
				} else {
					res = rollup.Day
				}
				from, to = from-from%res.Step, to-to%res.Step+res.Step
			}
			out := SeriesSet{Range: in.Range, Step: res.Step, From: from, To: to, Dim: in.Dim, Series: []Series{}}
			keys := []string{""}
			if in.Dim != rollup.DimTotal {
				top, err := d.Rollup.Top(ctx, in.Stream, res, in.Dim, from, to, in.Top)
				if err != nil {
					return nil, d.internalErr("throughput", err)
				}
				keys = keys[:0]
				for _, k := range top {
					keys = append(keys, k.Key)
				}
			}
			for _, k := range keys {
				pts, err := d.Rollup.Series(ctx, in.Stream, res, in.Dim, k, from, to)
				if err != nil {
					return nil, d.internalErr("throughput", err)
				}
				out.Series = append(out.Series, Series{Key: k, Points: pts})
			}
			return &struct{ Body SeriesSet }{out}, nil
		})

	huma.Register(api, huma.Operation{OperationID: "get-subject-tree", Method: http.MethodGet, Path: "/subjects/tree", Summary: "Subject tree level", Tags: []string{"rollups"}},
		func(ctx context.Context, in *struct {
			Stream string `query:"stream" default:"EVENTS" pattern:"^[A-Za-z0-9_-]{1,64}$"`
			Prefix string `query:"prefix" maxLength:"256" doc:"Parent path; empty for the roots"`
		}) (*struct {
			Body struct {
				Nodes []rollup.Node `json:"nodes"`
				Index IndexStats    `json:"index"`
			}
		}, error) {
			nodes, err := d.Rollup.Children(ctx, in.Stream, in.Prefix)
			if err != nil {
				return nil, d.internalErr("subject-tree", err)
			}
			out := &struct {
				Body struct {
					Nodes []rollup.Node `json:"nodes"`
					Index IndexStats    `json:"index"`
				}
			}{}
			out.Body.Nodes = nodes
			out.Body.Index.Indexed, out.Body.Index.Dropped, err = d.Rollup.IndexStats(ctx, in.Stream)
			out.Body.Index.Cap = d.Rollup.SubjectCap()
			if err != nil {
				return nil, d.internalErr("subject-tree", err)
			}
			return out, nil
		})

	huma.Register(api, huma.Operation{OperationID: "get-subject", Method: http.MethodGet, Path: "/subjects/info", Summary: "One indexed subject", Tags: []string{"rollups"}},
		func(ctx context.Context, in *struct {
			Stream  string `query:"stream" default:"EVENTS" pattern:"^[A-Za-z0-9_-]{1,64}$"`
			Subject string `query:"subject" required:"true" maxLength:"256"`
		}) (*struct{ Body rollup.SubjectInfo }, error) {
			s, err := d.Rollup.Subject(ctx, in.Stream, in.Subject)
			if errors.Is(err, sql.ErrNoRows) {
				return nil, huma.Error404NotFound("subject not indexed")
			}
			if err != nil {
				return nil, d.internalErr("subject", err)
			}
			return &struct{ Body rollup.SubjectInfo }{*s}, nil
		})
}
