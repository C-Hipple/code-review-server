import json
import subprocess
import sys
import time
from pprint import pprint
from typing import Any


def main():
    print("Starting codereviewserver...")
    # Start the server process
    process = subprocess.Popen(
        ["codereviewserver", "-server"],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=sys.stderr,
        text=True,
        bufsize=1,  # Line buffered
    )

    get_pr_params = [{"Repo": "gtdbot", "Owner": "C-Hipple", "Number": 25}]

    # send_request(process, "RPCHandler.GetAllReviews", [])
    send_request(process, "RPCHandler.GetPR", get_pr_params)
    read_response(process)


def send_request(process: Any, method: str, params: Any):
    # Send GetReviews request
    request = {"jsonrpc": "2.0", "method": method, "params": params, "id": 1}
    print(f"Sending request: {json.dumps(request)}")
    process.stdin.write(json.dumps(request) + "\n")
    process.stdin.flush()


def read_response(process: Any):
    try:
        # Wait a moment for the server to initialize
        time.sleep(1)
        # Read response
        print("Waiting for response...")
        response_line = process.stdout.readline()
        if response_line:
            try:
                response = json.loads(response_line)
                if "error" in response and response["error"]:
                    print(f"RPC Error: {response['error']}")
                elif "result" in response:
                    print("SUCCESS: Received response")
                    print(response["result"]["Content"])
                    # The content is in response["result"]["Content"]
                    # We might want to print it directly or handle it as needed
                    # with open("test.org", "w") as f:
                    #     f.write(response["result"]["Content"])
                    print("DONE CONTENT")
            except json.JSONDecodeError as e:
                print(f"Failed to decode JSON: {e}")
        else:
            print("No response received (EOF)")

    except KeyboardInterrupt:
        print("Interrupted")
    finally:
        print("Terminating server...")
        process.terminate()
        process.wait()


if __name__ == "__main__":
    main()
