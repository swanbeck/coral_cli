# Darwin CLI
The Darwin CLI is designed to simplify working with Docker images that are compatible with the Darwin ecosystem. It wraps the Docker CLI and extends it with several helpful commands.

---
### Building
To build the Darwin CLI, start by [installing Go](https://go.dev/doc/install). Once installation is verified, navigate to the [root directory](.) and run: 
```bash
go build
```
To create an executable `darwin`, instead run:
```bash
go build -o ./darwin
```
You can then start running commands with `./darwin`, i.e.
```bash
./darwin
```
Optionally, consider moving the `darwin` executable to a location on your `PATH` so it can be run from anywhere:
```bash
sudo mv ./darwin /usr/local/bin/
```
Verify it can be found with
```bash
darwin
```
which should print out
```bash
Darwin wraps Docker for the Darwin ecosystem

Usage:
  darwin [flags]
  darwin [command]

Available Commands:
  completion  Generate the autocompletion script for the specified shell
  help        Help about any command
  launch      Extract and run Darwin-compatible docker-compose services
  shutdown    Stop and remove resources for a given instance
  verify      Checks if a Docker image is compliant with Darwin's standards

Flags:
  -h, --help   help for darwin

Use "darwin [command] --help" for more information about a command.
```

---
### Usage
The Darwin CLI wraps certain Docker commands, including `darwin images` and `darwin ps`, to only show Darwin images or containers. However, most of its utility is relaized through a small set of bespoke commands:

#### Launch
Darwin launch takes in a valid Docker compose file and starts services listed within it in a particular manner. It first extracts runtime dependencies from all provided images using an embedded extraction entrypoint. To do this, the environment variable `DARWIN_LIB`, which controls where extracted files are placed, must be set to an absolute path in the local filesystem. Additionally, if running Darwin from inside a Docker container, the environment variable `DARWIN_IS_DOCKER=true` must be set and `DARWIN_HOST_LIB` must point to the same directory as `DARWIN_LIB` but within the host filesystem. For example, consider the following Docker compose file snippet:

```yaml
services:
  darwin_cli:
    image: darwin_cli-latest
    environment:
      - DARWIN_LIB=/home/darwin/lib
      - DARWIN_IS_DOCKER=true
      - DARWIN_HOST_LIB=/absolute/path/on/host/to/lib
    volumes:
      - /absolute/path/on/host/to/lib:/home/darwin/lib
```
These variables must be set because Darwin launches Docker containers using the host system's Docker installation. 

Once the proper environment variables are set, a Docker compose file can be created to start the relevant containers. The compose file to be run can be specified explicitly with `-f` or a local file `(docker-)compose.y(a)ml` will be used if exists and not overwritten. As an example, here is a valid Docker compose file that is compatible with Darwin launch:
```yaml
# compose.yaml
x-darwin-config:
  &darwin-config
  environment:
    - ROS_DOMAIN_ID=${INTERNAL_ROS_DOMAIN_ID}
    - RMW_IMPLEMENTATION=${INTERNAL_RMW_IMPLEMENTATION}
    - CYCLONEDDS_URI=${INTERNAL_CYCLONEDDS_URI}
    - DYNAMIC_LIBRARY=${INTERNAL_DYNAMIC_LIBRARY}
    - BT_FILE=${INTERNAL_BT_FILE}
    - CONFIG_PATH=${INTERNAL_CONFIG_PATH}
    - COMPOSE_TEMPLATE=${INTERNAL_COMPOSE_TEMPLATE}
    - HOST_CONFIG_PATH=${HOST_CONFIG_PATH}
    - DARWIN_LIBRARY_PATH=${INTERNAL_DYNAMIC_LIBRARY}
    - PARAMS=${INTERNAL_CONFIG_PATH}/params/main.yaml
  volumes:
    - ${HOST_CONFIG_PATH}:${INTERNAL_CONFIG_PATH}
  network_mode: host
  ipc: host
  tty: true

services:

  llama:
    image: ${PREFIX}-llama3.1_8b:${TAG}
    <<: *darwin-config
    environment:
      - ROS_DOMAIN_ID=${INTERNAL_ROS_DOMAIN_ID}
      - RMW_IMPLEMENTATION=${INTERNAL_RMW_IMPLEMENTATION}
      - CYCLONEDDS_URI=${INTERNAL_CYCLONEDDS_URI}
      - PARAMS=${INTERNAL_CONFIG_PATH}/params/main.yaml
      - AGENT=llama
    profiles: [skillsets]

  whisper:
    image: ${PREFIX}-whisper:${TAG}
    <<: *darwin-config
    environment:
      - ROS_DOMAIN_ID=${INTERNAL_ROS_DOMAIN_ID}
      - RMW_IMPLEMENTATION=${INTERNAL_RMW_IMPLEMENTATION}
      - CYCLONEDDS_URI=${INTERNAL_CYCLONEDDS_URI}
      - PARAMS=${INTERNAL_CONFIG_PATH}/params/main.yaml
      - AGENT=whisper
    profiles: [skillsets]

  gtts:
    image: ${PREFIX}-gtts:${TAG}
    <<: *darwin-config
    environment:
      - ROS_DOMAIN_ID=${INTERNAL_ROS_DOMAIN_ID}
      - RMW_IMPLEMENTATION=${INTERNAL_RMW_IMPLEMENTATION}
      - CYCLONEDDS_URI=${INTERNAL_CYCLONEDDS_URI}
      - PARAMS=${INTERNAL_CONFIG_PATH}/params/main.yaml
      - AGENT=gtts
    profiles: [skillsets]

  dynamic_runner:
    image: ${PREFIX}-dynamic_runner:${TAG}
    <<: *darwin-config
    profiles: [executors]
```

Importantly, Darwin assumes that the provided compose file uses the `profiles` tag. Only the profiles `drivers`, `skillsets`, and `executors` are used by Darwin; any other profiles used will be ignored. When running, `drivers` and `skillsets` are started first and `executors` are started a short time after. 

For example, running the command 

```bash
darwin launch
```
produces the following partial output:

```bash
📦 Extracting dependencies from image darwin-llama3.1_8b:amd64 for service llama
📦 Extracting dependencies from image darwin-whisper:amd64 for service whisper
📦 Extracting dependencies from image darwin-gtts:amd64 for service gtts
📦 Extracting dependencies from image darwin-dynamic_runner:amd64 for service dynamic_runner
📝 Writing merged compose file to: /home/darwin/config/lib/compose/darwin-1747512980139421567.yaml
🚀 Starting instance with name: darwin-1747512980139421567
🧰 Starting skillsets (3): [llama whisper gtts]
[+] Running 3/3
 ✔ Container darwin-1747512980139421567-whisper-1  Started                                      0.4s 
 ✔ Container darwin-1747512980139421567-llama-1    Started                                      0.4s
 ✔ Container darwin-1747512980139421567-gtts-1     Started                                      0.2s
⚙️  Starting executors (1): [dynamic_runner]
⏳ Delaying 1s before starting executors...
[+] Running 1/1
 ✔ Container darwin-1747512980139421567-dynamic_runner-1  Started                               0.2s
 ...
```

When running with `darwin launch`, it is often useful to assign a `-g` group or `--handle` to a running instance. Handles are meant to be unique for a single instance and groups allow you to control several instances together. These are especially useful when running in detached mode
```bash
darwin launch -g group1 -d
```

to enable easy shutdown.

#### Shutdown
Darwin shutdown exists to nicely kill and clean up after Darwin launch commands that are run in detached mode. For example, if a launch command is run
```bash
darwin launch -g group1 -d
```
it can be nicely killed and cleaned up with
```bash
darwin shutdown -g group1
```
Shutdown can also be controlled via an instance name that is generated and printed on Darwin launch with `-n` (`darwin-1747512980139421567` in the example output above) or using a `--handle` provided when Darwin launch is run. The `-a` flag can also be used to shutdown all running Darwin instances.

#### Verify
When building a Darwin-comptible Docker image, it is useful to test whether the image is compliant with the assumptions that are made with Darwin launch. To do this, you can use the command
```bash
darwin verify <IMAGE_NAME>:<IMAGE_TAG>
```
to test whether the provided image is compatible. 
