package mapview

import (
	"embed"
	"html/template"
)

//go:embed map.js
var staticFS embed.FS

func mustEmbed(name string) string {
	b, err := staticFS.ReadFile(name)
	if err != nil {
		panic("mapview: " + err.Error())
	}
	return string(b)
}

// pageTmpl — SSR-оболочка карты (pageHTML в handlers.go) со встроенным JS
// как именованным подшаблоном "mapjs" (второй Parse с тем же именем
// перезаписал бы корневой шаблон — поэтому t.New("mapjs")). HTML и тексты
// индексируемы, интерактив — client-side (issue #73, требование SEO).
var pageTmpl = func() *template.Template {
	t := template.Must(template.New("map").Parse(pageHTML))
	if _, err := t.New("mapjs").Parse(mustEmbed("map.js")); err != nil {
		panic("mapview: " + err.Error())
	}
	return t
}()
