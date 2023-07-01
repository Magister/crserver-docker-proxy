package main

import (
	"bytes"
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
	"strings"
	"time"
)

// CrsCall crs:call struct
type CrsCall struct {
	XMLName xml.Name `xml:"call"`
	Alias   string   `xml:"alias,attr"`
	Name    string   `xml:"name,attr"`
	Version string   `xml:"version,attr"`
}

var repoURL *url.URL
var repoIPByVersion = make(map[string]string)

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

	// build crserver URL
	var url = *repoURL
	destIP := ""
	ok := false
	if destIP, ok = repoIPByVersion[parsed.Version]; !ok {
		reportError(writer, fmt.Sprintf(`There is no %[1]s version of repository server installed on the server.`, parsed.Version))
		return
	}
	url.Host = net.JoinHostPort(destIP, "80")
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

	// get repo url
	repoURLString := os.Getenv("REPO_URL")
	if repoURLString == "" {
		log.Panicln("Please set REPO_URL environment variable to Configuration Repository URL")
	}

	// get listen port
	listenPort, ok := os.LookupEnv("LISTEN_PORT")
	if !ok {
		listenPort = "8080"
	}

	// connect to Docker
	repoPortsChan := DockerConnect()

	// parse url
	var err error
	repoURL, err = url.Parse(repoURLString)
	if err != nil {
		log.Panicln(err.Error())
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
	srv.ListenAndServe()
}
