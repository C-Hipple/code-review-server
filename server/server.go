package server

import (
	"codereviewserver/config"
	"codereviewserver/git_tools"
	"context"
	"fmt"
	"log/slog"
	"net/rpc"
	"net/rpc/jsonrpc"
	"os"
	// "strings"
)

// testing mutable state
// RPC handler is recreated for each request, it's not stateful across requests
// simulate a db lol
var CurrentCount int

func RunServer(log *slog.Logger) {
	server := rpc.NewServer()
	handler := &RPCHandler{Log: log}
	if err := server.Register(handler); err != nil {
		log.Error("Error registering RPC handler", "error", err)
		return
	}

	server.ServeCodec(jsonrpc.NewServerCodec(&Stdio{}))
}

type Stdio struct{}

func (s *Stdio) Read(p []byte) (n int, err error) {
	return os.Stdin.Read(p)
}

func (s *Stdio) Write(p []byte) (n int, err error) {
	return os.Stdout.Write(p)
}

func (s *Stdio) Close() error {
	return nil
}

type RPCHandler struct {
	Log *slog.Logger
}

type HelloArgs struct{}
type HelloReply struct {
	Count   int
	Content string
}

func (h *RPCHandler) Hello(args *HelloArgs, reply *HelloReply) error {
	var count int
	err := config.C.DB.QueryRow("SELECT COUNT(*) FROM sections").Scan(&count)
	if err != nil {
		h.Log.Error("Error counting items", "error", err)
		return err
	}
	CurrentCount += count
	reply.Content = fmt.Sprintf("hello %d", CurrentCount)
	reply.Count = count

	return nil
}

type GetReviewsArgs struct{}
type GetReviewsReply struct {
	Content string
}

func (h *RPCHandler) GetAllReviews(args *GetReviewsArgs, reply *GetReviewsReply) error {
	renderer := NewOrgRenderer(config.C.DB)
	content, err := renderer.RenderAllSectionsToString()
	if err != nil {
		h.Log.Error("Error rendering org files", "error", err)
		return err
	}
	reply.Content = content
	return nil
}

type GetPRstructArgs struct {
	Repo   string `json:"Repo"`
	Owner  string `json:"Owner"`
	Number int    `json:"Number"`
}

type GetPRReply struct {
	Okay    bool
	Content string
}

func (h *RPCHandler) GetPR(args *GetPRstructArgs, reply *GetPRReply) error {
	content, err := GetFullPRResponse(args.Owner, args.Repo, args.Number)
	if err != nil {
		h.Log.Error("Error fetching PR details", "error", err)
		return err
	}

	reply.Content = content
	reply.Okay = true
	return nil
}

type AddCommentArgs struct {
	Owner    string `json:"Owner"`
	Repo     string `json:"Repo"`
	Number   int    `json:"Number"`
	Filename string
	Position int64
	Body     string
}

type AddCommentReply struct {
	ID      int64
	Content string
}

func (h *RPCHandler) AddComment(args *AddCommentArgs, reply *AddCommentReply) error {
	commentID := config.C.DB.InsertLocalComment(args.Owner, args.Repo, args.Number, args.Filename, args.Position, &args.Body)
	reply.ID = commentID.ID

	// Return the updated PR body
	content, err := GetFullPRResponse(args.Owner, args.Repo, args.Number)
	if err != nil {
		h.Log.Error("Error fetching PR details", "error", err)
		return err
	}
	reply.Content = content
	return nil
}

type SetFeedbackArgs struct {
	Owner    string `json:"Owner"`
	Repo     string `json:"Repo"`
	Number   int    `json:"Number"`
	Body     string
}

type SetFeedbackReply struct {
	ID      int64
	Content string
}

func (h *RPCHandler) SetFeedback(args *SetFeedbackArgs, reply *SetFeedbackReply) error {
	config.C.DB.InsertFeedback(args.Owner, args.Repo, args.Number, &args.Body)

	// Return the updated PR body
	content, err := GetFullPRResponse(args.Owner, args.Repo, args.Number)
	if err != nil {
		h.Log.Error("Error fetching PR details", "error", err)
		return err
	}
	reply.Content = content
	return nil
}

type RemovePRCommentsArgs struct {
	Repo   string `json:"Repo"`
	Owner  string `json:"Owner"`
	Number int    `json:"Number"`
}

type RemovePRCommentsReply struct {
	Okay    bool
	Content string
}

func (h *RPCHandler) RemovePRComments(args *RemovePRCommentsArgs, reply *RemovePRCommentsReply) error {
	err := config.C.DB.DeleteLocalCommentsForPR(args.Owner, args.Repo, args.Number)
	if err != nil {
		h.Log.Error("Error removing local comments", "error", err)
		return err
	}
	reply.Okay = true

	// Return the updated PR body
	content, err := GetFullPRResponse(args.Owner, args.Repo, args.Number)
	if err != nil {
		h.Log.Error("Error fetching PR details", "error", err)
		return err
	}
	reply.Content = content
	return nil
}
