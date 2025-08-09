package cmd

import (
	"log"
	"os"
)

// logToFile handles logging to "app.log".
// If the file doesn't exist, it creates it.
// If it can't open/create the file, it logs to stderr instead.
func logToFile(message string) {
	// Open or create the file in append mode
	file, err := os.OpenFile("wget-log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0666)
	if err != nil {
		log.Println("Could not open or create log file, logging to stderr:", err)
		log.Println(message)
		return
	}
	defer file.Close()

	// Create a logger that writes to the file
	logger := log.New(file, "", log.LstdFlags)
	logger.Println(message)
}
