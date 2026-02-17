package main

import "log"

const runtimeDisabledReason = "project disabled due to abuse: all runtime features have been removed"

func main() {
	log.SetFlags(0)
	log.Println(runtimeDisabledReason)
}
