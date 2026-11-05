package httpapi

import (
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/yiluoiro/card-word/internal/store"
)

type dashboardPeriod struct {
	Key, From, To, Label string
	Start, End           time.Time
}

func parseDashboardPeriod(q url.Values, now time.Time) (dashboardPeriod, error) {
	today := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
	p := dashboardPeriod{Key: q.Get("period"), Start: time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC), End: today.AddDate(0, 0, 1), Label: "本月"}
	if q.Get("custom") == "1" {
		p.Key = "custom"
	}
	switch p.Key {
	case "", "month":
		p.Key = "month"
	case "7d":
		p.Start = today.AddDate(0, 0, -6)
		p.Label = "近 7 天"
	case "30d":
		p.Start = today.AddDate(0, 0, -29)
		p.Label = "近 30 天"
	case "last_month":
		p.End = p.Start
		p.Start = p.Start.AddDate(0, -1, 0)
		p.Label = "上月"
	case "custom":
		from, e1 := time.Parse("2006-01-02", q.Get("from"))
		to, e2 := time.Parse("2006-01-02", q.Get("to"))
		if e1 != nil || e2 != nil || to.Before(from) || to.After(today) || to.Sub(from) >= 366*24*time.Hour {
			return p, errors.New("请选择有效日期：开始不晚于结束，结束不晚于今天，跨度不超过 366 天。")
		}
		p.Start = from
		p.End = to.AddDate(0, 0, 1)
		p.Label = "自定义"
	default:
		return p, errors.New("不支持的统计周期。")
	}
	p.From = p.Start.Format("2006-01-02")
	p.To = p.End.AddDate(0, 0, -1).Format("2006-01-02")
	return p, nil
}
func periodChange(current, previous int64) string {
	if previous == 0 {
		if current == 0 {
			return "与上一周期持平"
		}
		return "上一周期为 0"
	}
	delta := (float64(current) - float64(previous)) / float64(previous) * 100
	if delta == 0 {
		return "与上一周期持平"
	}
	return fmt.Sprintf("较上一周期 %+.1f%%", delta)
}

type chartPoint struct {
	X, Y    float64
	Label   string
	Orders  int64
	Revenue string
}
type chartTick struct {
	Y     float64
	Value int64
}
type dashboardChart struct {
	Points     []chartPoint
	Ticks      []chartTick
	Line, Area string
}

func buildDashboardChart(days []store.DayStat, currency string) dashboardChart {
	c := dashboardChart{}
	var max int64 = 5
	for _, d := range days {
		if d.Orders > max {
			max = d.Orders
		}
	}
	step := (max + 4) / 5
	max = step * 5
	for i := 0; i <= 5; i++ {
		c.Ticks = append(c.Ticks, chartTick{24 + float64(i)*36, max - int64(i)*step})
	}
	for i, d := range days {
		x := 48.0
		if len(days) > 1 {
			x += float64(i) * 740 / float64(len(days)-1)
		}
		y := 204 - float64(d.Orders)/float64(max)*180
		label := d.Date
		if len(label) >= 10 {
			label = label[5:]
		}
		c.Points = append(c.Points, chartPoint{x, y, label, d.Orders, formatMoney(d.RevenueCents, currency)})
		c.Line += fmt.Sprintf("%.2f,%.2f ", x, y)
	}
	if len(days) > 0 {
		c.Area = "48,204 " + c.Line + "788,204"
	}
	return c
}
