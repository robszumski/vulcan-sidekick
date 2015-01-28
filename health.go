package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"time"
)

const (
	checkInterval = time.Second * 30
	maxSleep      = time.Minute * 5
)

type settings struct {
	debug         bool
	prefix        string
	siteName      string
  siteHostname  string
	backendName   string
	interval      int
	targetAddress string
	etcdAddress   string
}

type vulcanBackendInstancePayload struct {
	URL string `json:"URL"`
}

type vulcanBackendPayload struct {
	Type string `json:"Type"`
}

type vulcanFrontendPayload struct {
	Type        string `json:"Type"`
	BackendName string `json:"BackendId"`
	Route       string `json:"Route"`
}

var inService bool
var siteName string
var lastStatusCode int

func main() {
	fs := flag.NewFlagSet("sidekick-flagset", flag.ExitOnError)
	debug := fs.Bool("debug", false, "output debug info and log all attempted health checks")
	prefix := fs.String("prefix", "vulcand", "prefix of the etcd keyspace used by vulcand")
	siteName := fs.String("site-name", "", "label used to identify the site's backends and frontends")
  siteHostname := fs.String("site-hostname", "", "hostname of the site this backend serves")
	backendName := fs.String("backend-name", "", "identifier used for this instance of the backend app")
	interval := fs.Int("interval", 30, "how often to trigger the health check in seconds")
	targetAddress := fs.String("target-address", "", "address of the backend to be health checked")
	etcdAddress := fs.String("etcd-address", "http://localhost:4001", "address of the etcd cluster")

	if err := fs.Parse(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}

	if *targetAddress == "" {
		fmt.Fprintln(os.Stderr, "target-address is required")
		os.Exit(1)
	}
	//TODO: add these

	s := settings{
		debug:         *debug,
		prefix:        *prefix,
		siteName:      *siteName,
    siteHostname:  *siteHostname,
		backendName:   *backendName,
		interval:      *interval,
		targetAddress: *targetAddress,
		etcdAddress:   *etcdAddress,
	}

	//initialize variables
	healthy := false
	sleep := time.Second * time.Duration(s.interval)

	if s.debug {
		fmt.Printf("(debug) Using settings: %v\n", s)
	}

	//InitializeSite(&s)

	for {
		healthy = HealthCheck(&s)

		if healthy {
			if inService {
				if *debug {
					log.Printf("(debug) Healthy: %v returned HTTP %v, next check in %v seconds", s.targetAddress, lastStatusCode, sleep)
				}
			} else {
				//reset sleep counter
				sleep = time.Second * time.Duration(s.interval)
				log.Printf("Healthy: %v returned HTTP %v, next check in %v seconds", s.targetAddress, lastStatusCode, sleep)
				TriggerRecovery(&s)
			}
		} else {
			//calc backoff
			sleep = ExpBackoff(sleep, maxSleep)
			log.Printf("Failure: %v returned HTTP %v, backing off %v seconds", s.targetAddress, lastStatusCode, sleep)

			if inService {
				TriggerFailure(&s)
			}
		}
		time.Sleep(sleep)
	}
}

func TriggerRecovery(s *settings) {
	//create new etcd client
	ec := &etcdClient{
		Address: s.etcdAddress,
		Prefix:  "v2/keys/" + s.prefix,
	}

	//create our backend instance
	//TODO: abstract out
	p2 := vulcanBackendInstancePayload{
		URL: s.targetAddress,
	}
	backendInstancePath := "/backends/" + s.siteName + "/servers/" + s.backendName
	err2 := ec.Put(backendInstancePath, p2, s)
	if err2 != nil {
		fmt.Printf("error putting value to etcd: %v", err2)
		return
	}
	if s.debug {
		log.Printf("(debug) Vulcand backend instance for %s has been initialized.", s.backendName)
	}

	inService = true
	log.Printf("Target is healthy. Added to rotation.")
}

func TriggerFailure(s *settings) {
	//create new etcd client
	ec := &etcdClient{
		Address: s.etcdAddress,
		Prefix:  "v2/keys/" + s.prefix,
	}

	//delete the backend instance
	backendInstancePath := "/backends/" + s.siteName + "/servers/" + s.backendName
	err := ec.Delete(backendInstancePath, s)
	if err != nil {
		fmt.Printf("error deleting value from etcd: %v", err)
		return
	}

	inService = false
	log.Printf("Target is unhealthy. Removed from rotation.")
}

func InitializeSite(s *settings) {
	//create new etcd client
	ec := &etcdClient{
		Address: s.etcdAddress,
		Prefix:  "v2/keys/" + s.prefix,
	}

	//create our backend
	p1 := vulcanBackendPayload{
		Type: "http",
	}
	backendPath := "/backends/" + s.siteName + "/backend"
	err := ec.Put(backendPath, p1, s)
	if err != nil {
		fmt.Printf("Error creating backend %s: %v", s.siteName, err)
		return
	}
	if s.debug {
		log.Printf("(debug) Vulcand backend for %s has been initialized.", s.siteName)
	}

	//create our frontend instance
  routingRegix := fmt.Sprintf("Host('%s') && PathRegexp('/.*')", s.siteHostname)
	p2 := vulcanFrontendPayload{
		Type:        "http",
		BackendName: s.siteName,
		Route:       routingRegix,
	}
	frontendPath := "/frontends/" + s.siteName + "/frontend"
	err2 := ec.Put(frontendPath, p2, s)
	if err2 != nil {
		fmt.Printf("error putting value to etcd: %v", err2)
		return
	}
	if s.debug {
		log.Printf("(debug) Vulcand frontend for %s (%s) has been initialized.", s.siteName, s.siteHostname)
	}

	log.Printf("Frontend and backend for %s have been initialized.", s.siteName)

}

type etcdClient struct {
	Address string
	Prefix  string
}

func ConstructPut(path string, json string, s *settings) (*http.Response, error) {
  body := bytes.NewBuffer([]byte("value=" + json))

  req, err := http.NewRequest("PUT", path, body)
  if err != nil {
    return nil, err
  }

  req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

  resp, err := http.DefaultClient.Do(req)
  if err != nil {
    return nil, err
  }

  return resp, nil
}

func (c *etcdClient) Put(path string, value interface{}, s *settings) error {

  //stringify json
  json, err := json.Marshal(value)
  if err != nil {
    return err
  }

  //construct the request url
	u := fmt.Sprintf("%s/%s%s", c.Address, c.Prefix, path)

	resp, err := ConstructPut(u, string(json), s)

	if err != nil {
    return err
  }

  if s.debug {
    fmt.Printf("Status code from etcd is HTTP %d", resp.StatusCode)
  }

  if int(resp.StatusCode/100) == 3 {
    location := resp.Header["Location"][0]
    resp, err = ConstructPut(location, string(json), s)

    if err != nil {
      return err
    }
  }

  //check for any 50x
  if int(resp.StatusCode/100) == 5 {
    return fmt.Errorf("etcd unexpectedly returned HTTP %d \n", resp.StatusCode)
  }

  return nil
}

func (c *etcdClient) Delete(path string, s *settings) error {

	u := fmt.Sprintf("%s/%s%s", c.Address, c.Prefix, path)

	req, err := http.NewRequest("DELETE", u, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}

	defer resp.Body.Close()
	respBody, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if s.debug {
		fmt.Println(string(respBody))
	}

	//check for any non-20x
	if int(resp.StatusCode/100) == 5 {
		return fmt.Errorf("etcd unexpectedly returned HTTP %d \n", resp.StatusCode)
	}

	return nil
}

func HealthCheck(s *settings) bool {
	resp, err := http.Get(s.targetAddress)
	if err != nil {
		log.Printf("Error checking target %v: %v", s.targetAddress, err)
		return false
	}

	lastStatusCode = resp.StatusCode

	//check for any 20x
	if int(resp.StatusCode/100) == 2 {
		return true
	}

	return false
}

func ExpBackoff(prev, max time.Duration) time.Duration {
	if prev == 0 {
		return time.Second
	}
	if prev > max/2 {
		return max
	}
	return 2 * prev
}
