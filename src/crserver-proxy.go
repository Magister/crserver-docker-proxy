package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/akamensky/argparse"
	"github.com/judwhite/go-svc"
)

// program implements svc.Service
type program struct {
	wg   sync.WaitGroup
	quit chan struct{}
}

// CrsParams crs:params struct
type CrsParams struct {
	Comment string `xml:"comment"`
	Request string `xml:"request"`
}

// CrsCall crs:call struct
type CrsCall struct {
	XMLName xml.Name  `xml:"call"`
	Alias   string    `xml:"alias,attr"`
	Name    string    `xml:"name,attr"`
	Version string    `xml:"version,attr"`
	Params  CrsParams `xml:"params"`
}

var repoURL *url.URL
var repoIPByVersion = make(map[string]string)
var commitCommentRegexp *regexp.Regexp

func cloneHeaders(src, dst http.Header) {
	for name, values := range src {
		for _, value := range values {
			dst.Add(name, value)
		}
	}
}

func reportError(writer http.ResponseWriter, msg string) {
	err := fmt.Sprintf(`{
		{3ccb2518-9616-4445-aaa7-20048fead174,"%[1]s",
		{9f06d311-1431-4a54-bd6f-fa93c4d4c471,
		{9f06d311-1431-4a54-bd6f-fa93c4d4c471,"",
		{00000000-0000-0000-0000-000000000000},""}
		},"","000000000000000",00000000-0000-0000-0000-000000000000},17,
		{"file:////",0},"%[1]s"}`, strings.ReplaceAll(msg, "\"", "\"\""))
	writer.Header().Set("Content-Type", "application/xml")
	writer.WriteHeader(200)
	fmt.Fprintf(writer, `<?xml version="1.0" encoding="UTF-8"?>
<crs:call_exception xmlns:crs="http://v8.1c.ru/8.2/crs" clsid="3ccb2518-9616-4445-aaa7-20048fead174">%s</crs:call_exception>`, base64.StdEncoding.EncodeToString([]byte(err)))
}

func handleRequest(writer http.ResponseWriter, incoming *http.Request) {

	defer incoming.Body.Close()

	// read request body
	body, err := ioutil.ReadAll(incoming.Body)
	if err != nil {
		reportError(writer, err.Error())
		return
	}
	// parse xml
	var parsed CrsCall
	err = xml.Unmarshal(body, &parsed)
	if err != nil {
		writer.WriteHeader(403)
		return
	}

	// validate
	if len(parsed.Alias) == 0 || len(parsed.Name) == 0 || len(parsed.Version) == 0 {
		writer.WriteHeader(403)
		return
	}

	if parsed.Name == "DevDepot_enrollDevObjects" {
		comment := strings.TrimSpace(parsed.Params.Comment)
		if !commitCommentRegexp.MatchString(comment) {
			reportError(writer, fmt.Sprintf(`Commit message %[1]s does not conform to regexp %[2]s.`, comment, commitCommentRegexp.String()))
			return
		}
	}

	// build crserver URL
	var url = *repoURL
	destIP := ""
	ok := false
	if destIP, ok = repoIPByVersion[parsed.Version]; !ok {
		if destIP, ok = repoIPByVersion["*"]; !ok {
			reportError(writer, fmt.Sprintf(`There is no %[1]s version of repository server installed on the server.`, parsed.Version))
			return
		}
	}
	if _, _, ok := net.SplitHostPort(destIP); ok == nil {
		url.Host = destIP
	} else {
		url.Host = net.JoinHostPort(destIP, "80")
	}
	url.Path += incoming.RequestURI

	// proxy request to crserver
	client := http.Client{
		Timeout: time.Second * 1200,
	}
	req, _ := http.NewRequest("POST", url.String(), bytes.NewReader(body))
	cloneHeaders(incoming.Header, req.Header)
	resp, err := client.Do(req)
	if err != nil {
		reportError(writer, err.Error())
		return
	}
	defer resp.Body.Close()

	// proxy response back
	cloneHeaders(resp.Header, writer.Header())
	writer.WriteHeader(resp.StatusCode)
	io.Copy(writer, resp.Body)
}

func main() {

	prg := &program{}

	// Call svc.Run to start your program/service.
	if err := svc.Run(prg); err != nil {
		log.Fatal(err)
	}

}

func (p *program) Init(env svc.Environment) error {
	log.Printf("is win service? %v\n", env.IsWindowsService())
	return nil
}

func (p *program) Stop() error {
	// The Stop method is invoked by stopping the Windows service, or by pressing Ctrl+C on the console.
	// This method may block, but it's a good idea to finish quickly or your process may be killed by
	// Windows during a shutdown/reboot. As a general rule you shouldn't rely on graceful shutdown.

	log.Println("Stopping...")
	close(p.quit)
	p.wg.Wait()
	log.Println("Stopped.")
	return nil
}

func (p *program) Start() error {
	// The Start method must not block, or Windows may assume your service failed
	// to start. Launch a Goroutine here to do something interesting/blocking.

	p.quit = make(chan struct{})

	p.wg.Add(1)

	var repoURLStringArg *string
	var listenPortArg *string
	var commitRegexpStrArg *string

	parser := argparse.NewParser("crserver-proxy", "1C config repo proxy")
	repoURLStringArg = parser.String("u", "repo-url", &argparse.Options{
		Required: false,
		Help:     "Configuration Repository URL",
	})
	listenPortArg = parser.String("p", "port", &argparse.Options{
		Required: false,
		Help:     "Listen port",
	})
	commitRegexpStrArg = parser.String("r", "commit-regexp", &argparse.Options{
		Required: false,
		Help:     "RegExp to validate commit messages",
	})
	args_err := parser.Parse(os.Args)
	if args_err != nil {
		// In case of error print error and print usage
		// This can also be done by passing -h or --help flags
		fmt.Print(parser.Usage(args_err))
		os.Exit(1)
	}

	// collect parsed args
	var ok bool
	repoURLString := *repoURLStringArg
	listenPort := *listenPortArg
	commitRegexpStr := *commitRegexpStrArg

	// for not specified args, use env
	// get repo url
	if repoURLString == "" {
		repoURLString = os.Getenv("REPO_URL")
		if repoURLString == "" {
			log.Panicln("Please set REPO_URL environment variable to Configuration Repository URL")
		}
	}

	// get listen port
	if listenPort == "" {
		listenPort, ok = os.LookupEnv("LISTEN_PORT")
		if !ok {
			listenPort = "8080"
		}
	}

	// init commit msg regexp
	if commitRegexpStr == "" {
		commitRegexpStr, ok = os.LookupEnv("COMMIT_REGEXP")
		if !ok {
			commitRegexpStr = `.*`
			fmt.Printf("COMMIT_REGEXP env var not specified, using default %[1]s\n", commitRegexpStr)
		}
	}
	commitCommentRegexp = regexp.MustCompile(`(?i)` + commitRegexpStr)
	fmt.Printf("Using comit regexp %[1]s\n", commitCommentRegexp.String())

	// parse url
	var err error
	repoURL, err = url.Parse(repoURLString)
	if err != nil {
		log.Panicln(err.Error())
	}

	// connect to Docker if configured
	var repoPortsChan <-chan map[string]string
	dockerHost, ok := os.LookupEnv("DOCKER_HOST")
	if !ok || len(strings.TrimSpace(dockerHost)) == 0 {
		repoIPByVersion = make(map[string]string)
		repoIPByVersion["*"] = repoURL.Host
	} else {
		repoPortsChan = DockerConnect()
	}

	// update config on channel
	go func() {
		for {
			repoIPByVersion = <-repoPortsChan
			for k, v := range repoIPByVersion {
				fmt.Printf("%s => %s\n", k, v)
			}
		}
	}()

	// start webserver
	fmt.Printf("Listening port %s (to change use LISTEN_PORT env var)\n", listenPort)
	srv := &http.Server{
		Addr:         ":" + listenPort,
		ReadTimeout:  1200 * time.Second,
		WriteTimeout: 1200 * time.Second,
		IdleTimeout:  1200 * time.Second,
	}
	http.HandleFunc("/", handleRequest)
	go func() {
		log.Println("Starting...")
		go srv.ListenAndServe()
		<-p.quit
		log.Println("Quit signal received...")
		srv.Shutdown(context.Background())
		p.wg.Done()
	}()

	return nil
}
