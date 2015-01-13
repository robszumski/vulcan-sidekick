#!/usr/bin/python

import time, random, math, sys
import human_curl as hurl
from optparse import OptionParser

usage = "usage: health.py [options] arg1 arg2"
parser = OptionParser(usage=usage)
parser.add_option("--debug",
                  dest="debug", default=False,
                  help="output all attempted health checks [default: false]")
parser.add_option("--prefix",
                  dest="prefix", default="vulcand",
                  help="prefix of the etcd keyspace used by vulcand [default: vulcand]")
parser.add_option("--site-label",
                  dest="site",
                  help="label used to identify the site's backends and frontends")
parser.add_option("--backend-name",
                  dest="name",
                  help="identifier used for this instance of the backend app")
parser.add_option("--interval",
                  dest="interval", default=30,
                  help="how often to trigger the health check [default: 30 seconds]")
parser.add_option("--etcd",
                  dest="etcdAddress", default="http://127.0.0.1:4001",
                  help="address of the etcd cluster [default: http://120.0.0.1:4001]")
parser.add_option("--backend-address",
                  dest="targetAddress",
                  help="address of the backend to be health checked")
(options, args) = parser.parse_args(args=None, values=None)

if options.debug:
    print '(debug) Parsed arguments:'
    print options

#read args
failCount = 0
failStatus = None
currentBackoff = float(options.interval)
inService = False
address = 'http://httpbin.org/status/200'

#execute health check
def checkHealth():
    global options
    global failCount
    global lastCode
    r = hurl.get(
        str(options.targetAddress),
        allow_redirects=True
    )
    lastCode = r.status_code
    if r.status_code // 100 == 4:
         return False
    elif r.status_code // 100 == 5:
         return False
    else:
         return True

#initialize site
def initializeSite():
    global options
    
    #init backend
    try:
        etcdPath = '%s/v2/keys/%s/backends/%s/backend' % (str(options.etcdAddress), str(options.prefix), str(options.site))
        r1 = hurl.put(etcdPath, data={'value': '{"Type": "http"}'})
        if r1.status_code // 100 == 2:
            print 'Vulcand backend for %s has been initialized.' % str(options.site)
    except:
        print 'Error: Could not communicate with etcd to initialize site %s.' % str(options.site)
        if options.debug:
             print '(debug) Attempted etcd address is %s' % etcdPath
    
    #init frontend route
    try:
        etcdPath = '%s/v2/keys/%s/frontends/%s/frontend' % (str(options.etcdAddress), str(options.prefix), str(options.site))
        r2 = hurl.put(etcdPath, data={'value': '{"Type": "http", "BackendId": "%s", "Route": "Path(`/`)"}' % str(options.site)})
        if r2.status_code // 100 == 2:
            print 'Vulcand frontend for %s has been initialized.' % str(options.site)
    except:
        print 'Error: Could not communicate with etcd to initialize site %s.' % str(options.site)
        if options.debug:
             print '(debug) Attempted etcd address is %s' % etcdPath

#trigger failure
def triggerFailure():
    global options
    global inService
    global failCount

    try:
        etcdPath = '%s/v2/keys/%s/backends/%s/server/%s' % (str(options.etcdAddress), str(options.prefix), str(options.site), str(options.name))
        r = hurl.delete(etcdPath)
        if r.status_code // 100 == 2:
            inService = False
            print 'Target is unhealthy. Removed from rotation.'
    except:
        print 'Error: Could not communicate with etcd to remove target from rotation.'
        if options.debug:
             print '(debug) Attempted etcd address is %s' % etcdPath

#trigger recovery
def triggerRecovery():
    global options
    global inService

    etcdPath = '%s/v2/keys/%s/backends/%s/server/%s' % (str(options.etcdAddress), str(options.prefix), str(options.site), str(options.name))
    try:
        r = hurl.put(etcdPath, data={'value': '{"URL": "%s"}' % address})
        if r.status_code // 100 == 2:
            inService = True
            print 'Target is healthy. Added to rotation.'
    except:
        print 'Error: Could not communicate with etcd to add target back into rotation.'
        if options.debug:
             print '(debug) Attempted etcd address is %s' % etcdPath

#backoff after failure
def backoff():
    global options
    global failCount
    global currentBackoff
    if failCount >= 1:
        currentBackoff = math.ceil(math.exp(failCount))
    else:
        currentBackoff = float(options.interval)

    time.sleep(currentBackoff)

#trigger init
initializeSite()

#event loop
while True:

    healthy = checkHealth()


    if healthy:
        if inService:
             if options.debug:
                 print '(debug) Healthy: %s returned HTTP %s, next check in %s seconds' % (str(options.targetAddress), lastCode, currentBackoff)
        else:
            #add back to rotation and reset values
            failCount = 0
            currentBackoff = options.interval
            print 'Healthy: %s returned HTTP %s, next check in %s seconds' % (str(options.targetAddress), lastCode, currentBackoff)
            triggerRecovery()
    else:
        print 'Failure: %s returned HTTP %s, backing off %s seconds' % (str(options.targetAddress), lastCode, currentBackoff)
        failCount += 1
        if inService:
            #remove from rotation
            triggerFailure()

    backoff()
