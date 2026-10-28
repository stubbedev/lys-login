// Command lys-login is the lys desktop's display manager, greeter and lock screen.
package main

import "fmt"

func main() {
	fmt.Println(greet("world"))
}

func greet(name string) string {
	return "hello, " + name
}
