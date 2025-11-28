import subprocess
import time
import sys

def main():
    print("Starting codereviewserver...")
    # Start the server process
    process = subprocess.Popen(
        ['./codereviewserver', '-server'],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        # stderr=subprocess.STDOUT, # Merge stderr into stdout
        text=True,
        bufsize=1 # Line buffered
    )

    try:
        # Give it a moment to initialize if needed, though we can just start reading/writing
        responded = True
        initialized = False
        i = 0

        while True:
            print(i, responded, initialized)
            if responded and initialized:
                print("Sending 'hello' command...")
                process.stdin.write("hello\n")
                process.stdin.flush()
                responded = False

            # Read output
            while True:
                line = process.stdout.readline()
                if not line:
                    break

                line = line.strip()
                print(f"Server output: {line}")
                if "Starting RPC" in line:
                    initialized = True


                if line.startswith("hello "):
                    print("SUCCESS: Received hello response")
                    responded = True
                break
            i += 1
            time.sleep(0.1)

            # time.sleep(5)


    except KeyboardInterrupt:
        print("Interrupted")
    finally:
        print("Terminating server...")
        process.terminate()
        process.wait()

if __name__ == "__main__":
    main()
