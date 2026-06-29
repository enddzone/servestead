package main

import (
	"bytes"
	"fmt"
	"path"
	"strings"
	"text/template"

	"servestead/resources"
)

func mustReadResource(resourcePath string) string {
	content, err := resources.FS.ReadFile(resourcePath)
	if err != nil {
		panic(fmt.Sprintf("read embedded resource %s: %v", resourcePath, err))
	}
	return string(content)
}

func mustRenderResourceTemplate(resourcePath string, data any) string {
	tmpl, err := template.New(resourcePath).Funcs(template.FuncMap{
		"aptGet":               aptGetCommand,
		"jsonString":           jsonString,
		"join":                 strings.Join,
		"noninteractiveAptGet": noninteractiveAptGetCommand,
		"shellQuote":           shellQuote,
		"yamlDoubleQuote":      yamlDoubleQuote,
		"yamlSingleQuote":      yamlSingleQuote,
	}).ParseFS(resources.FS, resourcePath)
	if err != nil {
		panic(fmt.Sprintf("parse embedded resource template %s: %v", resourcePath, err))
	}
	var output bytes.Buffer
	if err := tmpl.ExecuteTemplate(&output, path.Base(resourcePath), data); err != nil {
		panic(fmt.Sprintf("render embedded resource template %s: %v", resourcePath, err))
	}
	return output.String()
}
