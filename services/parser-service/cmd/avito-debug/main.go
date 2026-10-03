// avito-debug: скачать страницу через реальный клиент и сохранить в файл.
// Использование: avito-debug -url <...> -out <file> [-proxies env]
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/yourusername/real-estate-analyzer/parser-service/internal/avito"
)

func main() {
	url := flag.String("url", "", "страница для скачивания")
	out := flag.String("out", "/tmp/page.html", "куда сохранить")
	flag.Parse()
	if *url == "" {
		fmt.Println("-url required")
		os.Exit(1)
	}
	var proxies []string
	if p := os.Getenv("AVITO_PROXIES"); p != "" {
		proxies = strings.Split(p, ",")
	}
	c, err := avito.NewClient(avito.ClientConfig{
		Proxies:        proxies,
		MinDelay:       2 * time.Second,
		MaxDelay:       4 * time.Second,
		BlockThreshold: 2,
		MaxRotations:   12,
	})
	if err != nil {
		fmt.Println("client:", err)
		os.Exit(1)
	}
	var body []byte
	for try := 1; try <= 6; try++ {
		body, err = c.Fetch(*url)
		if err == nil {
			break
		}
		fmt.Printf("попытка %d: %v\n", try, err)
		time.Sleep(3 * time.Second)
	}
	if err != nil {
		os.Exit(1)
	}
	if err := os.WriteFile(*out, body, 0o644); err != nil {
		fmt.Println("write:", err)
		os.Exit(1)
	}
	fmt.Printf("ok: %d bytes -> %s\n", len(body), *out)
}
