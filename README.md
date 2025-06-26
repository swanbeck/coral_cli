<div align="center">
  <img src="./.asset/coral.svg" width="30%">
</div>

# Coral CLI
Coral (COmposable Robotics Abstraction Layer) represents an effort toward software for robotics applications that is truly composable. Coral draws inspiration from functional programming to create reconfigurable systems composed of modular and reusable atomic components with minimal functional interfaces. This is achieved using behavior trees and containerization.

Just as coral reefs support tremendous biodiversity (25% of marine species while covering less than 1% of the sea floor), Coral provides the scaffolding necessary to support a rich ecosystem of robotics software that enables scalable solutions across a wide range of real-world applications.

Users are referred to [coral_examples](https://github.com/swanbeck/coral_examples.git) for examples of practical applications enabled by Coral.

<!-- The Coral CLI is designed to simplify working with Docker images that are compatible with the Coral ecosystem. It wraps the Docker CLI and extends it with several helpful commands.  -->

---
### Building
To build the Coral CLI, start by [installing Go](https://go.dev/doc/install). Once installation is verified, navigate to the [root directory](.) and run: 
```bash
go build
```
To create an executable `coral`, instead run:
```bash
go build -o ./coral
```
You can then start running commands with `./coral`, i.e.
```bash
./coral
```
Optionally, consider moving the `coral` executable to a location on your `PATH` so it can be run from anywhere:
```bash
sudo mv ./coral /usr/local/bin/
```
Verify it can be found with
```bash
coral
```
which should print out
```
Coral wraps Docker for the Coral ecosystem

Usage:
  coral [flags]
  coral [command]

Available Commands:
  completion  Generate the autocompletion script for the specified shell
  help        Help about any command
  launch      Extract and run Coral-compatible docker-compose services
  shutdown    Stop and remove resources for a given instance
  verify      Checks if a Docker image is compliant with Coral's standards

Flags:
  -h, --help   help for coral

Use "coral [command] --help" for more information about a command.
```

---
### Usage
The Coral CLI wraps certain Docker commands, including `coral images` and `coral ps`, to only show Coral images or containers. However, most of its utility is realized through a small set of bespoke commands:

#### Launch
Coral launch takes in a valid Docker compose file and starts services listed within it in a particular manner. It first extracts runtime dependencies from all provided images using an embedded extraction entrypoint. To do this, the environment variable `CORAL_LIB`, which controls where extracted files are placed, must be set to an absolute path in the local filesystem. Additionally, if running Coral from inside a Docker container, the environment variable `CORAL_IS_DOCKER=true` must be set and `CORAL_HOST_LIB` must point to the same directory as `CORAL_LIB` but within the host filesystem. For example, consider the following Docker compose file snippet:

```yaml
services:
  coral_cli:
    image: coral_cli-latest
    environment:
      - CORAL_LIB=/home/coral/lib
      - CORAL_IS_DOCKER=true
      - CORAL_HOST_LIB=/absolute/path/on/host/to/lib
    volumes:
      - /absolute/path/on/host/to/lib:/home/coral/lib
```
These variables must be set because Coral launches Docker containers using the host system's Docker installation. 

Once the proper environment variables are set, a Docker compose file can be created to start the relevant containers. The compose file to be run can be specified explicitly with `-f` or a local file `(docker-)compose.y(a)ml` will be used if exists and not overriden. As an example, here is a valid Docker compose file that is compatible with Coral launch:
```yaml
# compose.yaml
x-coral-config:
  &coral-config
  environment:
    - ROS_DOMAIN_ID=${INTERNAL_ROS_DOMAIN_ID}
    - RMW_IMPLEMENTATION=${INTERNAL_RMW_IMPLEMENTATION}
    - CYCLONEDDS_URI=${INTERNAL_CYCLONEDDS_URI}
    - DYNAMIC_LIBRARY=${INTERNAL_DYNAMIC_LIBRARY}
    - BT_FILE=${INTERNAL_BT_FILE}
    - CONFIG_PATH=${INTERNAL_CONFIG_PATH}
    - COMPOSE_TEMPLATE=${INTERNAL_COMPOSE_TEMPLATE}
    - HOST_CONFIG_PATH=${HOST_CONFIG_PATH}
    - CORAL_LIBRARY_PATH=${INTERNAL_DYNAMIC_LIBRARY}
    - PARAMS=${INTERNAL_CONFIG_PATH}/params/main.yaml
  volumes:
    - ${HOST_CONFIG_PATH}:${INTERNAL_CONFIG_PATH}
  network_mode: host
  ipc: host
  tty: true

services:

  llama:
    image: coral-llama3.1_8b:humble-amd64
    <<: *coral-config
    environment:
      - ROS_DOMAIN_ID=${INTERNAL_ROS_DOMAIN_ID}
      - RMW_IMPLEMENTATION=${INTERNAL_RMW_IMPLEMENTATION}
      - CYCLONEDDS_URI=${INTERNAL_CYCLONEDDS_URI}
      - PARAMS=${INTERNAL_CONFIG_PATH}/params/main.yaml
      - AGENT=llama
    profiles: [skillsets]

  whisper:
    image: coral-whisper:humble-amd64
    <<: *coral-config
    environment:
      - ROS_DOMAIN_ID=${INTERNAL_ROS_DOMAIN_ID}
      - RMW_IMPLEMENTATION=${INTERNAL_RMW_IMPLEMENTATION}
      - CYCLONEDDS_URI=${INTERNAL_CYCLONEDDS_URI}
      - PARAMS=${INTERNAL_CONFIG_PATH}/params/main.yaml
      - AGENT=whisper
    profiles: [skillsets]

  gtts:
    image: coral-gtts:humble-amd64
    <<: *coral-config
    environment:
      - ROS_DOMAIN_ID=${INTERNAL_ROS_DOMAIN_ID}
      - RMW_IMPLEMENTATION=${INTERNAL_RMW_IMPLEMENTATION}
      - CYCLONEDDS_URI=${INTERNAL_CYCLONEDDS_URI}
      - PARAMS=${INTERNAL_CONFIG_PATH}/params/main.yaml
      - AGENT=gtts
    profiles: [skillsets]

  dynamic_runner:
    image: coral-dynamic_runner:humble-amd64
    <<: *coral-config
    profiles: [executors]
```

Importantly, Coral assumes that the provided compose file uses the `profiles` tag. Only the profiles `drivers`, `skillsets`, and `executors` are used by Coral; any other profiles used will be ignored. When running, `drivers` and `skillsets` are started first and `executors` are started a short time after. 

For example, running the command 

```bash
coral launch
```
produces the following partial output:

```
📦 Extracting dependencies from image coral-llama3.1_8b:humble-amd64 for service llama
📦 Extracting dependencies from image coral-whisper:humble-amd64 for service whisper
📦 Extracting dependencies from image coral-gtts:humble-amd64 for service gtts
📦 Extracting dependencies from image coral-dynamic_runner:humble-amd64 for service dynamic_runner
Writing compose file /home/coral/config/lib/compose/coral-1747512980139421567.yaml
Starting instance coral-1747512980139421567
🧠 Starting skillsets (3): [llama whisper gtts]
[+] Running 3/3
 ✔ Container coral-1747512980139421567-whisper-1  Started                                      0.4s 
 ✔ Container coral-1747512980139421567-llama-1    Started                                      0.4s
 ✔ Container coral-1747512980139421567-gtts-1     Started                                      0.2s
🚀 Starting executors (1): [dynamic_runner]
Delaying 1s before starting executors...
[+] Running 1/1
 ✔ Container coral-1747512980139421567-dynamic_runner-1  Started                               0.2s
 ...
```

When running with `coral launch`, it is often useful to assign a `-g` group or `--handle` to a running instance. Handles are meant to be unique for a single instance and groups allow you to control several instances together. These are especially useful when running in detached mode
```bash
coral launch -g group1 -d
```

to enable easy shutdown.

#### Shutdown
Coral shutdown exists to nicely kill and clean up after Coral launch commands that are run in detached mode. For example, if a launch command is run
```bash
coral launch -g group1 -d
```
it can be nicely killed and cleaned up with
```bash
coral shutdown -g group1
```
Shutdown can also be controlled via an instance name that is generated and printed on Coral launch with `-n` (`coral-1747512980139421567` in the example output above) or using a `--handle` provided when Coral launch is run. The `-a` flag can also be used to shutdown all running Coral instances.

#### Verify
When building a Coral-comptible Docker image, it is useful to test whether the image is compliant with the assumptions that are made with Coral launch. To do this, you can use the command
```bash
coral verify <IMAGE_NAME>:<IMAGE_TAG>
```
to test whether the provided image is compatible. 
