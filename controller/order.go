package controller

import (
	"github.com/kataras/iris/v12"
	"kandaoni.com/anqicms/provider"
	"kandaoni.com/anqicms/response"
	"path/filepath"
	"strings"
)

func OrderIndexPage(ctx iris.Context) {
	currentSite := provider.CurrentSite(ctx)

	route := ctx.Params().GetStringDefault("route", "index")
	// 防止路径遍历
	route = filepath.Clean(route)
	if strings.Contains(route, "/") || strings.Contains(route, "\\") || strings.HasPrefix(route, ".") {
		ctx.StatusCode(iris.StatusNotFound)
		return
	}
	tpl := "order/" + route + ".html"
	tpl, ok := currentSite.TemplateExist(tpl)
	if !ok {
		ctx.StatusCode(iris.StatusNotFound)
		return
	}
	if webInfo, ok := ctx.Value("webInfo").(*response.WebInfo); ok {
		webInfo.Title = route
		webInfo.PageName = "order"
		ctx.ViewData("webInfo", webInfo)
	}

	ctx.ViewData("currentRoute", route)

	ctx.View(tpl)
}
