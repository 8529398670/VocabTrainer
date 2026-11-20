package routes

import (
	errors "errors"
	time "time"

	fiber "github.com/gofiber/fiber/v3"

	apidoc "vocabtrainer/server/apidoc"
	jobs "vocabtrainer/server/jobs"
	logs "vocabtrainer/server/logs"
)

// jobRow is one line of the Jobs screen: what jobs.Status says, less what
// only /metrics reads.
type jobRow struct {
	Name            string    `json:"name"`
	IntervalSeconds int       `json:"interval_seconds"`
	Running         bool      `json:"running"`
	Interrupted     bool      `json:"interrupted"`
	Runs            int       `json:"runs"`
	NextDue         time.Time `json:"next_due"`
	LastStarted     time.Time `json:"last_started,omitzero"`
	LastFinished    time.Time `json:"last_finished,omitzero"`
	LastOk          *bool     `json:"last_ok,omitempty"`
	LastNote        string    `json:"last_note,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
}

func ( jobRow ) APIName() ( name string ) { name = "Job"; return }

func newJobRow( status jobs.Status ) ( row jobRow ) {
	row = jobRow{
		Name:            status.Name,
		IntervalSeconds: status.IntervalSeconds,
		Running:         status.Running,
		Interrupted:     status.Interrupted,
		Runs:            status.Runs,
		NextDue:         status.NextDue,
		LastOk:          status.LastOK,
		LastNote:        status.LastNote,
		LastError:       status.LastError,
	}
	if status.LastStarted != nil { row.LastStarted = *status.LastStarted }
	if status.LastFinished != nil { row.LastFinished = *status.LastFinished }
	return
}

type jobsReply struct {
	Jobs []jobRow `json:"jobs"`
}

// listJobs is the owner's Jobs screen: what runs in the background and how the
// last run of each went.
//
// @route("GET /api/owner/jobs", summary="Background jobs", tag="Owner", role=OWNER, feature="jobs")
func ( handlers *Handlers ) listJobs( c fiber.Ctx , in *apidoc.None ) ( reply *jobsReply , err error ) {
	reply = &jobsReply{ Jobs: []jobRow{} }
	for _ , status := range handlers.Jobs.Status() {
		reply.Jobs = append( reply.Jobs , newJobRow( status ) )
	}
	return
}

// jobNamePath is the job a route under /api/owner/jobs/{name} acts on.
type jobNamePath struct {
	Name string `json:"name" path:"name" jsonschema_description:"The job's name, from GET /api/owner/jobs."`
}

// runJob starts a job now, ahead of its schedule. The answer is 202 because
// only the start is waited for: a backup can outlast a request, and the screen
// polls for how it ended.
//
// @route("POST /api/owner/jobs/{name}/run", summary="Run a job now",
//     tag="Owner", role=OWNER, feature="jobs", status=202, errors=(409,),
// )
func ( handlers *Handlers ) runJob( c fiber.Ctx , in *jobNamePath ) ( reply *okReply , err error ) {
	run_err := handlers.Jobs.RunNow( in.Name )
	switch {
	case errors.Is( run_err , jobs.ErrUnknown ):
		err = refuse( fiber.StatusNotFound , "not found" )
	case run_err != nil:
		err = refuse( fiber.StatusConflict , "job already running" )
	default:
		handlers.audit( c , logs.KindAdmin , "job_run" , fiber.Map{ "name": in.Name } )
		reply = &okReply{ Ok: true }
	}
	return
}
