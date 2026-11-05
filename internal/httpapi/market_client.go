package httpapi

import (
	"context"
	"github.com/yiluoiro/card-word/internal/market"
)

// The community edition consumes the remote catalogue; it never publishes one.
func (s *Server) marketEntries(ctx context.Context) ([]market.Entry, error) {
	if s.market == nil {
		return nil, nil
	}
	client := *s.market
	client.Builtin = nil
	return client.List(ctx, s.cfg.MarketURL)
}
