package server

import (
	"codereviewserver/config"
	"fmt"
	"log/slog"
	"net/rpc"
	"net/rpc/jsonrpc"
	"os"
	"strings"
)

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
	Message string
	Count   int
}

func (h *RPCHandler) Hello(args *HelloArgs, reply *HelloReply) error {
	var count int
	err := config.C.DB.QueryRow("SELECT COUNT(*) FROM sections").Scan(&count)
	if err != nil {
		h.Log.Error("Error counting items", "error", err)
		return err
	}
	reply.Message = fmt.Sprintf("hello %d", count)
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
	// client := git_tools.GetGithubClient()
	// diff := git_tools.GetPRDiff(client, args.Owner, args.Repo, args.Number)
	// comments, err := git_tools.GetPRComments(client, args.Owner, args.Repo, args.Number)
	// if err != nil {
	//	reply.Content = err.Error()
	//	reply.Okay = false
	//	return nil
	// }

		diffLines, _ := GetPRDiffWithInlineComments(args.Owner, args.Repo, args.Number)
		reply.Content = strings.Join(diffLines, "\n")

		reply.Okay = true
	return nil
}
