package controller

import (
	_ "embed"
	"net/http"
)

//go:embed i18n.js
var i18nJS string

//go:embed i18n.css
var i18nCSS string

const languageSelect = `<select class="language-select" data-language-select aria-label="Language"><option value="zh">中文</option><option value="en">English</option></select>`

func languageScript(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(i18nJS))
}

func languageStyles(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(i18nCSS))
}
