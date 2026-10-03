// tlsfetch — диагностическая утилита: GET с TLS-имперсонацией Chrome.
// Использование: tlsfetch <url> [<url>...] ; опционально заголовок через -header "X-Api-Key: ..."
package main

import (
	"fmt"
	"os"
	"strings"

	fhttp "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

func main() {
	var header string
	var urls []string
	for _, a := range os.Args[1:] {
		if strings.HasPrefix(a, "-header=") {
			header = strings.TrimPrefix(a, "-header=")
			continue
		}
		urls = append(urls, a)
	}
	cl, err := tlsclient.NewHttpClient(tlsclient.NewNoopLogger(),
		tlsclient.WithClientProfile(profiles.Chrome_150),
		tlsclient.WithTimeoutSeconds(20),
		tlsclient.WithInsecureSkipVerify(),
	)
	if err != nil {
		fmt.Println("client:", err)
		os.Exit(1)
	}
	for _, u := range urls {
		req, err := fhttp.NewRequest("GET", u, nil)
		if err != nil {
			fmt.Println("req:", err)
			continue
		}
		if header != "" {
			k, v, _ := strings.Cut(header, ":")
			req.Header.Set(strings.TrimSpace(k), strings.TrimSpace(v))
		}
		resp, err := cl.Do(req)
		if err != nil {
			fmt.Printf("%s -> ERR %v\n", u, err)
			continue
		}
		buf := make([]byte, 800)
		n, _ := resp.Body.Read(buf)
		resp.Body.Close()
		fmt.Printf("%s -> HTTP %d\n%.500s\n\n", u, resp.StatusCode, string(buf[:n]))
	}
}
