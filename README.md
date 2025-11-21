<div align="center">
    <a href="https://github.com/swanbeck/coral_cli/blob/main/"><img src="https://img.shields.io/github/last-commit/swanbeck/coral_cli" /></a>
    <a href="https://github.com/swanbeck/coral_cli/releases"><img src="https://img.shields.io/github/v/release/swanbeck/coral_cli?label=version" /></a>
    <a href="https://github.com/swanbeck/coral_cli/blob/main/LICENSE"><img src="https://img.shields.io/github/license/swanbeck/coral_cli" /></a>
    <a href="https://github.com/swanbeck/coral_cli/blob/main/"><img src="https://img.shields.io/badge/Linux-FCC624?logo=linux&logoColor=black" /></a>
    <a href="https://arxiv.org/abs/2509.02453"><img src="https://img.shields.io/badge/Paper-B34700?logo=google-scholar&logoColor=white" /></a>
    <br />
    <br />
</div>

<div align="center">
    <img src="./.asset/coral.svg" width="20%">
</div>

# Coral CLI
Coral (COmpositional Robotics Abstraction Layer) represents an effort toward truly composable software for robotics applications. Coral draws inspiration from functional programming to create reconfigurable systems composed of modular and reusable atomic components with minimal functional interfaces. This is achieved using behavior trees and containerization.

Just as coral reefs support tremendous biodiversity (25% of marine species while covering less than 1% of the sea floor), Coral provides the scaffolding necessary to support a rich ecosystem of robotics software that enables scalable solutions across a wide range of real-world applications.

Users are referred to [coral_examples](https://github.com/swanbeck/coral_examples.git) for examples of practical applications enabled by Coral.

<!-- The Coral CLI is designed to simplify working with Docker images that are compatible with the Coral ecosystem. It wraps the Docker CLI and extends it with several helpful commands.  -->

---
### Building
To build the Coral CLI, start by [installing Go](https://go.dev/doc/install). Once installation is verified, navigate to the [root directory](.) and run: 
```
go build
```
To create an executable `coral`, instead run:
```
go build -o ./coral
```
You can then start running commands with `./coral`, i.e.
```
./coral
```
Optionally, consider moving the `coral` executable to a location on your `PATH` so it can be run from anywhere:
```
sudo mv ./coral /usr/local/bin/
```
Verify it can be found with
```
coral
```
which should print out
```
Coral provides and manages an ecosystem of composable robotics software

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
      - CORAL_UID=1001
      - CORAL_GID=1001
    volumes:
      - /absolute/path/on/host/to/lib:/home/coral/lib
```
These variables must be set because Coral launches Docker containers using the host system's Docker installation. `CORAL_UID` and `CORAL_GID` can also be set to ensure proper ownership of extracted files when running Coral as a non-default user (1000:1000). 

Once the proper environment variables are set, a Docker compose file can be created to start the relevant containers. The compose file to be run can be specified explicitly with `-f` or a local file `(docker-)compose.y(a)ml` will be used if it exists and is not explicitly overridden. As an example, here is a valid Docker compose file that is compatible with Coral launch:
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

```
coral launch
```
produces the following partial output:

```
[INFO] Launching new instance coral-d3db75df-a1ec-41ba-bad5-2f4169b38ce1
[INFO] Starting skillsets (3): [llama whisper gtts]
[+] Running 3/3
 ✔ Container coral-d3db75df-a1ec-41ba-bad5-2f4169b38ce1-whisper-1  Started
 ✔ Container coral-d3db75df-a1ec-41ba-bad5-2f4169b38ce1-llama-1    Started
 ✔ Container coral-d3db75df-a1ec-41ba-bad5-2f4169b38ce1-gtts-1     Started
[INFO] Starting executors (1): [dynamic_runner]
[+] Running 1/1
 ✔ Container coral-d3db75df-a1ec-41ba-bad5-2f4169b38ce1-dynamic_runner-1  Started
 ...
```

When running with `coral launch`, it is often useful to assign a `-g` group or `--handle` to a running instance. Handles are meant to be unique for a single instance and groups allow you to control several instances together. These are especially useful when running in detached mode
```
coral launch -g group1 -d
```

to enable easy shutdown.

#### Shutdown
Coral shutdown exists to nicely kill and clean up after Coral launch commands that are run in detached mode. For example, if a launch command is run
```
coral launch -g group1 -d
```
it can be nicely killed and cleaned up with
```
coral shutdown -g group1
```
Shutdown can also be controlled via an instance name that is generated and printed on Coral launch with `-n` (`coral-1747512980139421567` in the example output above) or using a `--handle` provided when Coral launch is run. The `-a` flag can also be used to shutdown all running Coral instances.

#### Verify
When building a Coral component, it is useful to test whether it is compatible with the Coral CLI. To do this, you can use the command:
```
coral verify <IMAGE_NAME>:<IMAGE_TAG>
```
