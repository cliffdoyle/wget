package cmd

import (
	"log"
	"os"

	"github.com/spf13/cobra"
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

var LogDownload = &cobra.Command{
	Use:     "B",
	Aliases: []string{"b"},
	Short:   "log output to wget-log",
	Long:    "download a file and log the output in the wget-log file",
	Args:    cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		// call our main function with a way of calling our logger above
		// this is a test
		logToFile("this is a test by me")
	},
}

func init() {
	rootCmd.AddCommand(LogDownload)
}
