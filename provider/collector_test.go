package provider

import (
	"github.com/PuerkitoBio/goquery"
	"kandaoni.com/anqicms/model"
	"kandaoni.com/anqicms/response"
	"log"
	"strings"
	"testing"
)

func TestCollectSingleArticle(t *testing.T) {
	link := &response.WebLink{Url: "https://www.techug.com/post/golang-python-php-c-java-nodejs/"}
	keyword  := &model.Keyword{Title: "golang"}
	result, err := CollectSingleArticle(link, keyword)

	if err != nil {
		log.Println(err)
	}

	log.Printf("%#v", result.Content)
}

func TestGoquery(t *testing.T) {
	str := "<p>来张图中吧：</p>\n<p><img data-original=\"https://cdn.jiler.cn/techug/uploads/2017/03/420532-20170305205228282-609193437-1000x519.png\" title=\"图0：2017年的golang、python、php、c++、c、java、Nodejs性能对比\" alt=\"图0：2017年的golang、python、php、c++、c、java、Nodejs性能对比\"/></p>\n<p>总结：</p>"

	htmlR := strings.NewReader(str)
	doc, err := goquery.NewDocumentFromReader(htmlR)

	if err != nil {
		t.Fatal(err)
	}

	doc.Find("img").Each(func(i int, item *goquery.Selection) {
		src, _ := item.Attr("src")
		dataSrc, exists2 := item.Attr("data-src")
		if exists2 {
			src = dataSrc
		}
		dataSrc, exists2 = item.Attr("data-original")
		if exists2 {
			src = dataSrc
		}
		log.Println(src, dataSrc)
		log.Println(item.Parent().Html())
	})
}