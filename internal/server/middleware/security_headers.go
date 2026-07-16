package middleware

import "github.com/gin-gonic/gin"

// SecurityHeaders applies a browser baseline that is compatible with the
// statically exported Next.js management UI. TLS/HSTS remains the responsibility
// of the deployment's reverse proxy because this server may run plain HTTP.
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Content-Security-Policy", "default-src 'self'; base-uri 'self'; object-src 'none'; frame-ancestors 'none'; frame-src 'none'; form-action 'self'; img-src 'self' data: blob:; font-src 'self' data:; style-src 'self' 'unsafe-inline'; style-src-attr 'unsafe-inline'; script-src 'self' 'unsafe-inline'; script-src-attr 'none'; connect-src 'self'; manifest-src 'self'; worker-src 'self' blob:; media-src 'none'")
		c.Header("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Next()
	}
}
