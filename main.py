import subprocess
import time
import sys
import select

def main():
    print("Starting codereviewserver...")
    # Start the server process
    process = subprocess.Popen(
        ['codereviewserver', '-server'],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,  # Capture stderr for log messages
        text=True,
        bufsize=1 # Line buffered
    )

    try:
        # Wait a moment for the server to initialize
        time.sleep(3)
        print("Sending 'getReviews' command...")
        process.stdin.write("getReviews\n")
        process.stdin.flush()

        # Read the response
        content_lines = []
        if sys.platform != 'win32':
            # Use select to read all available data with timeout
            timeout = 5.0  # 5 second timeout
            start_time = time.time()
            while time.time() - start_time < timeout:
                ready, _, _ = select.select([process.stdout], [], [], 0.1)
                if ready:
                    line = process.stdout.readline()
                    if not line:
                        break
                    content_lines.append(line)
                elif content_lines:
                    # No more data available and we have some content, we're done
                    break
        else:
            # On Windows, read with a reasonable limit
            for _ in range(1000):
                line = process.stdout.readline()
                if not line:
                    break
                content_lines.append(line)

        if content_lines:
            print("SUCCESS: Received getReviews response")
            print("Content:")
            print("".join(content_lines), end='')
            print("DONE CONTENT")
        else:
            print("No response received")

    except KeyboardInterrupt:
        print("Interrupted")
    finally:
        print("Terminating server...")
        process.terminate()
        process.wait()

if __name__ == "__main__":
    main()
