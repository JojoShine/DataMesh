package httpserver

import (
	"github.com/gin-gonic/gin"
	"os"
	"path/filepath"
)

func registerWeb(r *gin.Engine) {
	directory := os.Getenv("WEB_DIR")
	if directory == "" {
		return
	}
	index := filepath.Join(directory, "index.html")
	if _, err := os.Stat(index); err != nil {
		return
	}
	r.Static("/static", filepath.Join(directory, "static"))
	page := func(c *gin.Context) {
		c.Header("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'")
		c.File(index)
	}
	r.GET("/", page)
	r.GET("/login", page)
	r.GET("/guides", page)
	r.GET("/guides/exchange", page)
	r.GET("/guides/governance", page)
	r.GET("/guides/assets", page)
	r.GET("/guides/security", page)
	r.GET("/assets/:id", page)
	r.GET("/platform/:platform/*path", page)
}
