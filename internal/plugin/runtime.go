package plugin

import (
	"log"
	"os"
	"strings"

	"github.com/docker/go-plugins-helpers/volume"
)

const socketAddress = "glusterfs"
const propagatedMount = "/var/lib/glusterfs-volume"
const stateFile = "/var/lib/glusterfs-volume/.glusterfs-plugin/volumes.json"

// Main configures and runs the Docker volume plugin process.
func Main(version, revision string) {
	configureLogging()
	if len(os.Args) == 3 && os.Args[1] == internalMountProbeArgument {
		os.Exit(runInternalMountProbe(os.Args[2]))
	}
	log.Printf("Starting GlusterFS Volume Plugin version=%s revision=%s", version, revision)
	defaultServers := splitList(os.Getenv("GFS_SERVERS"))
	defaultVolume := strings.TrimSpace(os.Getenv("GFS_VOLUME"))
	if err := ensureDirPath(propagatedMount, 0o755); err != nil {
		log.Fatalf("failed to prepare mount root: %v", err)
	}

	store := newStateStore(stateFile)
	volumes, err := store.load()
	if err != nil {
		log.Fatalf("failed to load state: %v", err)
	}

	driver := &glusterfsDriver{
		root:           propagatedMount,
		store:          store,
		volumes:        volumes,
		mounts:         map[string]*activeMount{},
		recoveryIssues: map[string]error{},
		defaultVolume:  defaultVolume,
		defaultServers: defaultServers,
		client:         &glfsConnector{},
		mountInfo:      procMountInfoReader{path: mountInfoPath},
		healthProbe:    subprocessMountHealthProbe{timeout: defaultMountProbeTimeout},
	}
	driver.reconcileStartup()

	handler := volume.NewHandler(driver)
	log.Printf("GlusterFS Volume Plugin listening on %s.sock", socketAddress)
	if err := handler.ServeUnix(socketAddress, 0); err != nil {
		log.Print(err)
	}
}

func configureLogging() {
	log.SetFlags(0)
	logfile := os.Getenv("LOGFILE")
	if logfile == "" {
		return
	}
	file, err := os.OpenFile(logfile, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o666)
	if err != nil {
		log.Fatalf("error opening log file: %v", err)
	}
	log.SetOutput(file)
}
