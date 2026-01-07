package workflows

import (
	"crs/config"
	"crs/org"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"os"
	"path/filepath"
	"syscall"
)

// waitTimeout waits for the WaitGroup for the specified duration.
// It returns true if the wait timed out, false otherwise.
func waitTimeout(wg *sync.WaitGroup, timeout time.Duration) bool {
	c := make(chan struct{})
	go func() {
		defer close(c)
		wg.Wait()
	}()
	select {
	case <-c:
		return false
	case <-time.After(timeout):
		return true
	}
}

type ManagerService struct {
	Workflows     []Workflow
	workflow_chan chan FileChanges
	sleepTime     time.Duration
	oneoff        bool
}

func deduplicateChanges(log *slog.Logger, changes []SerializedFileChange) []SerializedFileChange {
	changesByIdentifier := make(map[string][]SerializedFileChange)
	for _, change := range changes {
		identifier := change.FileChange.Item.Identifier()
		changesByIdentifier[identifier] = append(changesByIdentifier[identifier], change)
	}

	finalChanges := []SerializedFileChange{}
	log.Debug("Deduplicating changes", "count", len(changesByIdentifier))

	for identifier, itemChanges := range changesByIdentifier {
		var updateChange *SerializedFileChange
		var addChange *SerializedFileChange
		var deleteChange *SerializedFileChange

		for i, change := range itemChanges {
			switch change.FileChange.ChangeType {
			case "Addition":
				addChange = &itemChanges[i]
			case "Update", "Archive":
				updateChange = &itemChanges[i]
			case "Delete":
				deleteChange = &itemChanges[i]
			}
		}

		if updateChange != nil {
			log.Debug("Found update, discarding other changes", "identifier", identifier)
			finalChanges = append(finalChanges, *updateChange)
		} else if addChange != nil {
			log.Debug("Found add, discarding delete", "identifier", identifier)
			finalChanges = append(finalChanges, *addChange)
		} else if deleteChange != nil {
			log.Debug("Found delete", "identifier", identifier)
			finalChanges = append(finalChanges, *deleteChange)
		}
	}
	return finalChanges
}

func ListenChanges(log *slog.Logger, channel chan FileChanges, wg *sync.WaitGroup) {
	changesMap := make(map[string][]SerializedFileChange)
	for fileChange := range channel {
		if fileChange.ChangeType == "No Change" {
			wg.Done()
			continue
		}
		fileChange.Report(log)
		key := fileChange.Section.Name()
		changesMap[key] = append(changesMap[key], fileChange.Deserialize())
	}

	var serialziedChannel = make(chan SerializedFileChange)
	go ApplyChanges(log, serialziedChannel, wg)

	for _, changes := range changesMap {
		deduplicatedChanges := deduplicateChanges(log, changes)
		numDeduplicated := len(changes) - len(deduplicatedChanges)
		if numDeduplicated > 0 {
			log.Debug("Deduplicated changes, adjusting WaitGroup", "count", numDeduplicated)
			for i := 0; i < numDeduplicated; i++ {
				wg.Done()
			}
		}
		for _, change := range deduplicatedChanges {
			serialziedChannel <- change
		}
	}
	close(serialziedChannel)
}

func ApplyChanges(log *slog.Logger, channel chan SerializedFileChange, wg *sync.WaitGroup) {
	for deserializedChange := range channel {
		log.Info("Doing deser change: " + deserializedChange.FileChange.Item.ID())
		db := config.C.DB
		doc := org.NewDBClient(db, deserializedChange.FileChange.ItemSerializer)
		switch deserializedChange.FileChange.ChangeType {
		case "Addition":
			doc.AddDeserializedItemInSection(deserializedChange.FileChange.Section.Name(), deserializedChange.Lines)
		case "Update", "Archive":
			doc.UpdateDeserializedItemInSection(deserializedChange.FileChange.Section.Name(), deserializedChange.FileChange.Item, deserializedChange.FileChange.ChangeType == "Archive", deserializedChange.Lines)
		case "Delete":
			doc.DeleteItemInSection(deserializedChange.FileChange.Section.Name(), deserializedChange.FileChange.Item)
		}
		wg.Done()

	}

}

func NewManagerService(workflows []Workflow, oneoff bool, sleepTime time.Duration) ManagerService {
	used_workflows := []Workflow{}
	for _, wf := range workflows {
		if strings.Contains(fmt.Sprintf("%T", wf), "ListMyPRsWorkflow") {
			// TODO: match the release getter with the repo
			fixed := wf.(ListMyPRsWorkflow)
			used_workflows = append(used_workflows, fixed)
		} else {
			used_workflows = append(used_workflows, wf)
		}
	}

	return ManagerService{
		Workflows:     used_workflows,
		workflow_chan: make(chan FileChanges),
		sleepTime:     sleepTime,
		oneoff:        oneoff,
	}
}

func (ms ManagerService) runWorkflow(log *slog.Logger, workflow Workflow, workflow_chan chan FileChanges, file_change_wg *sync.WaitGroup) {
	// Helper which times the workflow run command.
	log.Info("Starting Workflow", "workflow", workflow.GetName())
	start := time.Now()
	result, err := workflow.Run(log, workflow_chan, file_change_wg)
	duration := time.Since(start)
	if err != nil {
		log.Error("Errored in Workflow", "workflow", workflow.GetName(), "after", duration, "error", err)
	}
	log.Info("Finishing Workflow", "workflow", workflow.GetName(), "took", duration, "result", result.Report())
}

func (ms ManagerService) RunOnce(log *slog.Logger, file_change_wg *sync.WaitGroup) {
	var wg sync.WaitGroup
	for _, workflow := range ms.Workflows {
		wg.Add(1)
		go func(workflow Workflow) {
			defer wg.Done()
			ms.runWorkflow(log, workflow, ms.workflow_chan, file_change_wg)
		}(workflow)
	}
	if waitTimeout(&wg, 240*time.Second) {
		log.Error("RunOnce waitgroup timed out waiting for workflows")
	} else {
		log.Info("Completed RunOnce Waitgroup")
	}
}

func (ms ManagerService) Run(log *slog.Logger) {
	log.Info("Starting Service")

	// Advisory lock to prevent multiple concurrent syncs
	home, err := os.UserHomeDir()
	if err == nil {
		lockPath := filepath.Join(home, ".config/codereviewserver_sync.lock")
		lockFile, err := os.Create(lockPath)
		if err == nil {
			err = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
			if err != nil {
				log.Warn("Another instance is already running background sync, skipping sync in this process.")
				lockFile.Close()
				return
			}
			defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
			defer lockFile.Close()
		}
	}

	if ms.oneoff {
		var listener_wg sync.WaitGroup
		listener_wg.Add(1)
		go ListenChanges(log, ms.workflow_chan, &listener_wg)

		log.Info("Running Once")
		ms.RunOnce(log, &listener_wg)
		close(ms.workflow_chan)
		listener_wg.Done()
		if waitTimeout(&listener_wg, 240*time.Second) {
			log.Error("Listener waitgroup timed out waiting for changes to be applied")
		}
	} else {
		cycle_count := 0
		log.Info("Starting service mode with sleep duration:" + ms.sleepTime.String())
		for {
			log.Info("Cycle", "count", cycle_count)
			var cycle_wg sync.WaitGroup
			cycle_wg.Add(1)
			ms.workflow_chan = make(chan FileChanges)

			go ListenChanges(log, ms.workflow_chan, &cycle_wg)
			ms.RunOnce(log, &cycle_wg)
			close(ms.workflow_chan)
			cycle_wg.Done()

			if waitTimeout(&cycle_wg, 240*time.Second) {
				log.Error("Cycle waitgroup timed out waiting for changes to be applied")
			}
			// Render org files after each cycle
			time.Sleep(ms.sleepTime)
			cycle_count++
		}
	}
	log.Info("Exiting Service")
}

func (ms *ManagerService) Initialize() {
	// Ensure all required sections exist.
	// Does this sync since GetSection has creation side effect
	db := config.C.DB
	for _, wf := range ms.Workflows {
		// Don't need to check release command here
		doc := org.NewDBClient(db, org.BaseOrgSerializer{ReleaseCheckCommand: ""})
		doc.GetSection(wf.GetOrgSectionName())
	}
}
