package main

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

const (
	numberOfWorker = 10

	// regex for parsing nginx.conf for domains
	re_domain = `^\s+server_name\s+([a-zA-z0-9._-]+);$`

	// we test against specific webservers. Only domains will be tested which
	// resolves to one of these
	expectedIPOfWebserver1 = "x.x.x.x."
	expectedIPOfWebserver2 = "x.x.x.x."

	// we test two uri against a domain.
	uri1 = "enterURI1"
	uri2 = "enterURI2"

	// format of prefixed timestamp in log messages
	timeLogFormat = "2006-01-02 15:04:05"

	// format of timestamp in log file names
	timeFileFormat = "20060102-1504"

	// Used in testUriAvailability for custom http.Client
	httpClientTimeout = 5
)

// Call this program with a configuration file of a webserver as it's one and only argument.
// You can feed this program with different config files, but you have to edit the constant re_domain.
// Output will be written into two files in the same directory where program is located. Log files
// will have a timestamp in its name as defined by constant timeFileFormat.
func main() {

	ex, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error determining absolute path of executable.\n")
		os.Exit(1)
	}

	if len(os.Args) != 2 {
		fmt.Printf("Usage: %s [config file]\n", ex)
		os.Exit(0)
	}

	execDir := filepath.Dir(ex)

	// we count domainWorker goroutines
	var wg sync.WaitGroup

	// every error message gets counted
	var errCount uint16

	// domainC gets read by domainWorker
	domainC := make(chan string)

	// status messages and listing of working URLs
	logC := make(chan string)

	// error log messages (failing URLs)
	errC := make(chan string)

	// logFinishedC channel signals that every log message was processed and cleanups were done
	logFinishedC := make(chan struct{})
	// errLogFinishedC channel signals that every error message was processed and cleanups were done
	errLogFinishedC := make(chan struct{})

	start := time.Now()

	fileFormatStartTime := start.Format(timeFileFormat)

	errorLogFile := fmt.Sprintf("%s/error.%s.log", execDir, fileFormatStartTime)
	go handleErrorLog(&errorLogFile, &errCount, errC, errLogFinishedC)

	mainLogFileFile := fmt.Sprintf("%s/main.%s.log", execDir, fileFormatStartTime)
	go handleMainLog(&mainLogFileFile, logC, logFinishedC)

	logC <- fmt.Sprintf("%s --> Starting\n", start.Format(timeLogFormat))

	wg.Add(numberOfWorker)
	for i := 0; i < numberOfWorker; i++ {
		go domainWorker(domainC, logC, errC, &wg)
	}

	foundDomains := parseDomainsFromConf()
	for domain := range foundDomains {
		domainC <- domain
	}

	close(domainC)

	wg.Wait()

	// close channel and block until all error log messages were processed
	close(errC)
	<-errLogFinishedC

	elapsed := time.Since(start)

	logC <- fmt.Sprintf("%s --> Finished\n", time.Now().Format(timeLogFormat))
	logC <- fmt.Sprintf("\t\t    --> Duration: %s\n", elapsed)
	logC <- fmt.Sprintf("\t\t    --> Domains:  %d\n", len(foundDomains))
	logC <- fmt.Sprintf("\t\t    --> Error:\t  %d\n", errCount)

	// close channel and block until all normal log messages were processed
	close(logC)
	<-logFinishedC

	if errCount == 0 {
		os.Exit(0)
	} else {
		os.Exit(1)
	}
}

// parseDomainsFromConf returns a map with valid domains as its keys from the config file
// which was provided as argument due program call. re_domain is used to find valid domains.
// The value of every key in the returned map is always an empty struct.
func parseDomainsFromConf() map[string]struct{} {

	domains := make(map[string]struct{})

	f, err := os.Open(os.Args[1])
	if err != nil {
		panic(fmt.Sprintf("Error opening provided config file %s.\n", os.Args[1]))
	}
	defer f.Close()

	re := regexp.MustCompile(re_domain)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		matchedLines := re.FindStringSubmatch(line)
		if len(matchedLines) == 2 {
			domains[matchedLines[1]] = struct{}{}
		}
	}

	return domains
}

// domainWorker fetches domains from domainC and performs basic domain tests before giving the domain
// to testUriAvailability which performs deeper url checks on potential domains
func domainWorker(domainC <-chan string, logC chan<- string, errC chan<- string, wg *sync.WaitGroup) {

	for domain := range domainC {
		IPaddresses, err := net.LookupHost(domain)
		if err != nil {
			errC <- fmt.Sprintf("%s '%s': (e01) Problem getting A records: %v\n", time.Now().Format(timeLogFormat), domain, IPaddresses)
			continue
		}

		if len(IPaddresses) != 1 {
			errC <- fmt.Sprintf("%s '%s': (e02) Got more than one returned IP address : %v\n", time.Now().Format(timeLogFormat), domain, IPaddresses)
			continue
		}

		if IPaddresses[0] == expectedIPOfWebserver1 || IPaddresses[0] == expectedIPOfWebserver2 {
			if ok := testUriAvailability(&domain, errC, wg); ok {
				logC <- fmt.Sprintf("%s '%s': ok\n", time.Now().Format(timeLogFormat), domain)
			}
		} else {
			errC <- fmt.Sprintf("%s '%s': (e03) A record resolves to wrong IP address: %v\n", time.Now().Format(timeLogFormat), domain, IPaddresses[0])
		}

	}
	
	wg.Done()	
}

// testUriAvailability tests the availability concurrently of uri1 and uri2 and returns true if both tests
// againts the uri's are successful. One domain can generate two (uri1+uri2) error log messages.
func testUriAvailability(domain *string, errC chan<- string, wg *sync.WaitGroup) bool {

	// for concatenating both complete urls
	var buffer bytes.Buffer

	// Result gets passed throug Channel uri1C and uri2C
	type Result struct {
		Message *http.Response
		Error   error
	}

	// function will block until each goroutines pass their result
	// through its channel
	uri1C := make(chan Result)
	uri2C := make(chan Result)

	// custom http.Client with shorter timeout
	cusHttpClient := &http.Client{
		Timeout: time.Duration(httpClientTimeout * time.Second),
		// if we don't want to be redirected
		//CheckRedirect: func(req *http.Request, via []*http.Request) error {
		//	return fmt.Errorf("Redirected!")
		//},
	}

	buffer.WriteString("http://")
	buffer.WriteString(*domain)
	buffer.WriteString(uri1)

	go func(url string) {
		var url1 Result
		url1.Message, url1.Error = cusHttpClient.Get(url)
		url1.Message.Body.Close()
		uri1C <- url1
	}(buffer.String())

	buffer.Reset()
	buffer.WriteString("http://")
	buffer.WriteString(*domain)
	buffer.WriteString(uri2)

	go func(url string) {
		var url2 Result
		url2.Message, url2.Error = cusHttpClient.Get(url)
		url2.Message.Body.Close()
		uri2C <- url2
	}(buffer.String())

	uri1Result := <-uri1C
	if uri1Result.Error != nil {
		errC <- fmt.Sprintf("%s '%s': (e04) Problem receiving uri1-resource: %s\n", time.Now().Format(timeLogFormat), domain, uri1Result.Error)
	}

	uri2Result := <-uri2C
	if uri2Result.Error != nil {
		errC <- fmt.Sprintf("%s '%s': (e05) Problem receiving uri2-resource: %s\n", time.Now().Format(timeLogFormat), domain, uri2Result.Error)
	}

	return uri1Result.Error == nil && uri2Result.Error == nil
}

// handleMainLog reads messages from logC and writes to mainLogFile. After closing logC handleMainLog will
// append the last status message with the number of "ok" domains to the mainLogFile
func handleMainLog(mainLogFile *string, logC <-chan string, logFinishedC chan<- struct{}) {

	// We start at -5 because 5 default status messages are processed every run
	okCount := -5

	f, err := os.OpenFile(*mainLogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		panic(fmt.Sprintf("Error opening %s for writing.\n", *mainLogFile))
	}

	for msg := range logC {
		_, err = f.WriteString(msg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error writing to file %s\n", *mainLogFile)
		}
		okCount++
	}

	_, err = f.WriteString(fmt.Sprintf("\t\t    --> Ok:\t  %d\n", okCount))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error writing to file %s\n", *mainLogFile)
	}

	f.Close()
	close(logFinishedC)
}

// handleErrorLog reads error messages from errC and writes to errorLogFile
func handleErrorLog(errorLogFile *string, errCount *uint16, errC <-chan string, errLogFinishedC chan<- struct{}) {
	f, err := os.OpenFile(*errorLogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		panic(fmt.Sprintf("Error opening %s for writing.\n", *errorLogFile))
	}

	for msg := range errC {
		_, err = f.WriteString(msg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error writing to file %s\n", *errorLogFile)
		}
		*errCount++
	}

	f.Close()
	close(errLogFinishedC)
}
