package server

import (
	"codereviewserver/config"
	"codereviewserver/org"
	"fmt"
	"log/slog"
	"net/rpc"
	"net/rpc/jsonrpc"
	"os"
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
	renderer := org.NewOrgRenderer(config.C.DB, org.BaseOrgSerializer{})
	content, err := renderer.RenderAllFilesToString()
	if err != nil {
		h.Log.Error("Error rendering org files", "error", err)
		return err
	}
	reply.Content = content
	return nil
}


type GetPRstructArgs struct {
	Repo string
	Number int
}

type GetPRReply struct {
	Content string
}


func (h *RPCHandler) Get(args *GetPRstructArgs, reply *GetPRReply) error {

	return nil
}
