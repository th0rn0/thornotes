package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"github.com/th0rn0/thornotes/internal/security"
)

// ShareThrottleMiddleware rate-limits the anonymous share surfaces, one bucket
// per (client IP, token) — see security.ShareRateLimiter for why the bucket is
// keyed that way and what the budgets mean.
//
// It is mounted on all four public routes (/s/:token, /f/:token, /f/:token/d/*,
// /f/:token/n/*) because they are one surface: throttling only the folder
// listing would leave the note reads under the same token — and the "More" cursor
// walk behind them — as an unbounded amplifier.
//
// A refusal is the surface's uniform empty 404, never 429: the design of this
// surface is that every failure is indistinguishable from "no such link", and a
// 429 (or a Retry-After) would tell the holder of a burned URL that the token was
// real. The throttle is logged so an operator can still see it.
func ShareThrottleMiddleware(limiter *security.ShareRateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !limiter.Enabled() {
			c.Next()
			return
		}

		ip := limiter.IP(c.Request)
		token := c.Param("token")
		if allowed, budget := limiter.Allow(ip, token); !allowed {
			// Operators need this line: the refusal it accompanies is, by
			// design, identical to every other 404 on the surface, so nothing
			// else in the logs says a link was being hammered. The token is
			// logged as the same short handle the access log writes, and the
			// path as the route pattern, so the log carries no capability.
			log.Warn().
				Str("ip", ip).
				Str("token", security.ShareTokenHandle(token)).
				Str("budget", budget).
				Str("route", c.FullPath()).
				Msg("share read throttled; answering the uniform 404")
			refuseAnonymousShare(c)
			return
		}

		c.Next()
	}
}
