package api

import (
	"net/http"

	"sideyard/internal/platform"
)

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.p.Jobs(r.PathValue("name"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (s *Server) createJob(w http.ResponseWriter, r *http.Request) {
	var spec platform.JobSpec
	if !readJSON(w, r, &spec) {
		return
	}
	if err := s.p.CreateJob(r.PathValue("name"), spec); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) updateJob(w http.ResponseWriter, r *http.Request) {
	var spec platform.JobSpec
	if !readJSON(w, r, &spec) {
		return
	}
	if err := s.p.UpdateJob(r.PathValue("name"), r.PathValue("job"), spec); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteJob(w http.ResponseWriter, r *http.Request) {
	if err := s.p.DeleteJob(r.PathValue("name"), r.PathValue("job")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) runJob(w http.ResponseWriter, r *http.Request) {
	run, err := s.p.RunJob(r.PathValue("name"), r.PathValue("job"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

func (s *Server) jobRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.p.JobRuns(r.PathValue("name"), r.PathValue("job"), 50)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}
