// Package controllers holds the HTTP handlers.
package controllers

import (
	"fullstack"
	auth "fullstack-passport"
	"fullstack/web"
)

// page is the data every page gets: the app name, the user and the CSRF field.
func page(app *fullstack.App, c *web.Ctx, extra map[string]any) map[string]any {
	data := map[string]any{"App": app.Config.Name, "CSRFField": auth.CSRFField(c)}
	if u, ok := auth.User(c); ok {
		data["User"] = u
	}
	for k, v := range extra {
		data[k] = v
	}
	return data
}

// Home serves the public pages.
type Home struct{ App *fullstack.App }

// Index is the home page.
func (h Home) Index(c *web.Ctx) error { return c.HTML(200, "pages/home", page(h.App, c, nil)) }

// Dashboard is the page of logged-in users.
func (h Home) Dashboard(c *web.Ctx) error { return c.HTML(200, "pages/dashboard", page(h.App, c, nil)) }
