// portdial: голый TCP-dial до хоста:порт N раз с паузой — отделить
// доступность шлюза от поведения http-клиента.
package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"time"
)

func main() {
	host := os.Args[1]
	port := os.Args[2]
	n := 3
	if len(os.Args) > 3 {
		n, _ = strconv.Atoi(os.Args[3])
	}
	for i := 1; i <= n; i++ {
		t0 := time.Now()
		c, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 8*time.Second)
		if err != nil {
			fmt.Printf("попытка %d [%s]: ОТКАЗ (%v)\n", i, time.Since(t0).Round(time.Millisecond), err)
		} else {
			c.Close()
			fmt.Printf("попытка %d [%s]: OK\n", i, time.Since(t0).Round(time.Millisecond))
		}
		time.Sleep(15 * time.Second)
	}
}
