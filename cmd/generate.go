package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"coral_cli/internal/logging"
)

var generateOutputDir string

func init() {
	generateCmd.Flags().StringVarP(&generateOutputDir, "output", "o", ".", "Parent directory in which to create the component (default: current directory)")
}

var generateCmd = &cobra.Command{
	Use:   "generate <name>",
	Short: "Scaffold a new Coral skillset component",
	Long: `Generates a complete skeleton for a Coral skillset component.

The generated component includes:
  - A multi-stage Dockerfile with build and export stages
  - Three ROS2 packages: <name>_interfaces, <name>, and <name>_behaviors
  - A working example: a Ping service on the node, called by a Ping behavior
  - runtime/ with a launch file and default parameters
  - compose.yaml and .env for building and running with Docker Compose

The skeleton compiles and exports a single BehaviorTree.CPP plugin (.so) that
calls the bundled node via a custom ROS2 service. Replace the Ping example with
your own logic.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if err := validateComponentName(name); err != nil {
			return err
		}
		if err := generateComponent(name, generateOutputDir); err != nil {
			return fmt.Errorf("%s: %w", logging.Failure("generation failed"), err)
		}
		return nil
	},
}

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func validateComponentName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("name %q must be lowercase snake_case (start with a letter; letters, digits, and underscores only)", name)
	}
	return nil
}

// toPascalCase converts snake_case to PascalCase (e.g. my_component → MyComponent)
func toPascalCase(s string) string {
	parts := strings.Split(s, "_")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "")
}

func writeGeneratedFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating directory %s: %w", filepath.Dir(path), err)
	}
	return os.WriteFile(path, []byte(content), 0644)
}

func generateComponent(name, outputDir string) error {
	root := filepath.Join(outputDir, "coral_"+name)
	if _, err := os.Stat(root); err == nil {
		return fmt.Errorf("directory %q already exists", root)
	}

	class := toPascalCase(name) // e.g. "my_component" → "MyComponent"
	r := strings.NewReplacer(
		"{{NAME}}", name,
		"{{CLASS}}", class,
	)

	type genFile struct {
		rel     string
		content string
		mode    os.FileMode
	}

	files := []genFile{
		{".env", genEnv(), 0644},
		{"compose.yaml", r.Replace(genComposeYAML()), 0644},
		{"docker/Dockerfile", r.Replace(genDockerfile()), 0644},
		{"docker/export/.keep", "", 0644},
		{"runtime/run.sh", r.Replace(genRunSh()), 0755},
		{"runtime/run.launch.py", r.Replace(genLaunchPy()), 0644},
		{"runtime/default/params.yaml", r.Replace(genParamsYAML()), 0644},
		// interfaces package
		{"src/" + name + "/" + name + "_interfaces/CMakeLists.txt", r.Replace(genInterfacesCMake()), 0644},
		{"src/" + name + "/" + name + "_interfaces/package.xml", r.Replace(genInterfacesPkgXML()), 0644},
		{"src/" + name + "/" + name + "_interfaces/srv/Ping.srv", genPingSrv(), 0644},
		// node package
		{"src/" + name + "/" + name + "/CMakeLists.txt", r.Replace(genNodeCMake()), 0644},
		{"src/" + name + "/" + name + "/package.xml", r.Replace(genNodePkgXML()), 0644},
		{"src/" + name + "/" + name + "/include/" + name + "/" + name + "_node.hpp", r.Replace(genNodeHpp()), 0644},
		{"src/" + name + "/" + name + "/src/" + name + "_node.cpp", r.Replace(genNodeCpp()), 0644},
		// behaviors package
		{"src/" + name + "/" + name + "_behaviors/CMakeLists.txt", r.Replace(genBehaviorsCMake()), 0644},
		{"src/" + name + "/" + name + "_behaviors/package.xml", r.Replace(genBehaviorsPkgXML()), 0644},
		{"src/" + name + "/" + name + "_behaviors/include/" + name + "_behaviors/ping.hpp", r.Replace(genPingHpp()), 0644},
		{"src/" + name + "/" + name + "_behaviors/src/ping.cpp", r.Replace(genPingCpp()), 0644},
		{"src/" + name + "/" + name + "_behaviors/src/plugin.cpp", r.Replace(genPluginCpp()), 0644},
	}

	for _, f := range files {
		path := filepath.Join(root, f.rel)
		if err := writeGeneratedFile(path, f.content); err != nil {
			return fmt.Errorf("writing %s: %w", f.rel, err)
		}
		if f.mode != 0644 {
			if err := os.Chmod(path, f.mode); err != nil {
				return fmt.Errorf("chmod %s: %w", f.rel, err)
			}
		}
	}

	fmt.Println(logging.Success(fmt.Sprintf("Generated coral_%s/", name)))
	fmt.Printf("\nNext steps:\n")
	fmt.Printf("  cd coral_%s\n", name)
	fmt.Printf("  docker compose build          # builds the image\n")
	fmt.Printf("  coral verify coral-%s:{tag}  # checks the image is Coral-compliant\n\n", name)
	fmt.Printf("The Ping example is a working end-to-end skeleton. Replace the service\n")
	fmt.Printf("definition, node logic, and behavior with your own implementation.\n")
	fmt.Printf("Search for TODO comments throughout the generated files for guidance.\n")
	return nil
}

// ---------------------------------------------------------------------------
// Top-level files
// ---------------------------------------------------------------------------

func genEnv() string {
	return `# TODO: Set VERSION to the coral-btcpp image tag you want to build on.
# Available tags: https://hub.docker.com/r/swanbeck/coral-btcpp/tags
VERSION=2.1.2
`
}

func genComposeYAML() string {
	return `services:
  {{NAME}}:
    # TODO: Update the image name to match your Docker Hub username / registry.
    image: coral-{{NAME}}:${VERSION}
    build:
      context: ./
      dockerfile: docker/Dockerfile
      platforms:
        - linux/amd64
        - linux/arm64
      target: coral
      args:
        VERSION: ${VERSION}
    network_mode: host
    ipc: host
`
}

// ---------------------------------------------------------------------------
// Dockerfile
// ---------------------------------------------------------------------------

func genDockerfile() string {
	return `ARG VERSION=unknown
FROM swanbeck/coral-btcpp:${VERSION} AS base

ARG VERSION=unknown
LABEL org.opencontainers.image.title="coral-{{NAME}}"
LABEL org.opencontainers.image.version="${VERSION}"
LABEL org.opencontainers.image.description="TODO: Add a description for your component."

USER root

# TODO: Install any additional apt packages your component needs.
# RUN apt-get update && apt-get install -y \
#     <your-package> \
#     && rm -rf /var/lib/apt/lists/*

COPY ./src/ /home/coral/ws/src/
WORKDIR /home/coral/ws
RUN ROS_DISTRO=$(basename /opt/ros/*) && \
    . /opt/ros/${ROS_DISTRO}/setup.sh && \
    colcon build --packages-up-to {{NAME}} && \
    rm -rf build/ log/ src/

COPY ./runtime /home/coral/runtime
RUN chmod +x /home/coral/runtime/run.sh && \
    chown -R coral:coral /home/coral/runtime

USER coral
RUN echo "source /home/coral/ws/install/local_setup.bash" >> ~/.bashrc && \
    echo "PS1='\[\e[1;33m\][{{NAME}}]\[\e[0m\]\[\e[1;32m\]\u@\h\[\e[0m\]:\[\e[1;34m\]\w\[\e[0m\]\$ '" >> ~/.bashrc

ENTRYPOINT [ "/home/coral/runtime/run.sh" ]

# ---- export stage ----
FROM base AS coral
USER root

# Create the export directory and populate it from a fresh build.
COPY ./docker/export /coral_lib
RUN mkdir -p /coral_lib/behaviors /coral_lib/interfaces

WORKDIR /home/coral/export_ws
COPY ./src ./src
RUN ROS_DISTRO=$(basename /opt/ros/*) && \
    . /opt/ros/${ROS_DISTRO}/setup.sh && \
    colcon build --packages-up-to {{NAME}}_behaviors --cmake-args -DBUILD_SHARED_LIBS=ON && \
    cp -r install/{{NAME}}_behaviors/lib/* /coral_lib/behaviors/ && \
    cp -r install/{{NAME}}_interfaces/lib/* /coral_lib/interfaces/ && \
    rm -rf /home/coral/export_ws

# CRITICAL: world read+execute on /coral_lib so the CLI can copy from it,
# and set CORAL_EXPORT_LIB so 'coral verify' knows where to look.
RUN chmod o+rx /coral_lib
ENV CORAL_EXPORT_LIB=/coral_lib
LABEL coral.profile="skillsets"

WORKDIR /home/coral/runtime
USER coral
`
}

// ---------------------------------------------------------------------------
// Runtime files
// ---------------------------------------------------------------------------

func genRunSh() string {
	return `#!/bin/bash
set -e

ROS_DISTRO=$(ls /opt/ros)
. /opt/ros/${ROS_DISTRO}/setup.bash
. /home/coral/ws/install/local_setup.bash

ros2 launch /home/coral/runtime/run.launch.py

exec "$@"
`
}

func genLaunchPy() string {
	return `#!/usr/bin/env python3
import os
from launch import LaunchDescription
from launch.actions import (
    DeclareLaunchArgument,
    GroupAction,
    OpaqueFunction,
    RegisterEventHandler,
    EmitEvent,
)
from launch.event_handlers import OnProcessExit
from launch.events import Shutdown
from launch.substitutions import LaunchConfiguration
from launch_ros.actions import Node, PushRosNamespace


def launch_setup(context, *args, **kwargs):
    agent = LaunchConfiguration("agent").perform(context)
    params = LaunchConfiguration("params").perform(context)
    log_level = LaunchConfiguration("log_level").perform(context)

    # TODO: Add additional nodes here if your component needs them.
    {{NAME}}_node = Node(
        package="{{NAME}}",
        executable="{{NAME}}_node",
        parameters=[params],
        arguments=["--ros-args", "--log-level", log_level],
        output="screen",
        emulate_tty=True,
    )

    actions = [{{NAME}}_node]

    if agent != "":
        actions.insert(0, PushRosNamespace(agent))

    return [
        GroupAction(actions),
        RegisterEventHandler(
            event_handler=OnProcessExit(
                target_action={{NAME}}_node,
                on_exit=[EmitEvent(event=Shutdown())],
            )
        ),
    ]


def generate_launch_description():
    return LaunchDescription(
        [
            DeclareLaunchArgument(
                "agent",
                default_value=f'{os.getenv("AGENT", "")}',
                description="Namespace applied to all processes. Set via the AGENT env var or launch argument.",
            ),
            DeclareLaunchArgument(
                "params",
                default_value=f'{os.getenv("PARAMS", "/home/coral/runtime/default/params.yaml")}',
                description="Path to the ROS2 parameters file.",
            ),
            DeclareLaunchArgument(
                "log_level",
                default_value=f'{os.getenv("LOG_LEVEL", "INFO")}',
                description="Log level for all processes.",
            ),
            OpaqueFunction(function=launch_setup),
        ]
    )
`
}

func genParamsYAML() string {
	return `# Default ROS2 parameters for the {{NAME}} component.
# This file is loaded by run.launch.py at startup.
# Override it at runtime by setting the PARAMS environment variable in compose.yaml.
#
# Parameter paths follow the pattern:
#   /**:           applies to all nodes in all namespaces
#     <node_name>:
#       ros__parameters:
#         <key>: <value>

/**:
  {{NAME}}_node:
    ros__parameters:
      # TODO: Replace 'prefix' with the parameters your node actually needs.
      # The Ping service prepends this string to every reply ("Pong: <message>").
      prefix: "Pong"
`
}

// ---------------------------------------------------------------------------
// {{NAME}}_interfaces package
// ---------------------------------------------------------------------------

func genInterfacesCMake() string {
	return `cmake_minimum_required(VERSION 3.8)
project({{NAME}}_interfaces)

if(CMAKE_COMPILER_IS_GNUCXX OR CMAKE_CXX_COMPILER_ID MATCHES "Clang")
  add_compile_options(-Wall -Wextra -Wpedantic)
endif()

find_package(ament_cmake REQUIRED)
find_package(rosidl_default_generators REQUIRED)

# TODO: Add additional .msg or .srv files here interfaces are added.
set(srv_files
  "srv/Ping.srv"
)

rosidl_generate_interfaces(${PROJECT_NAME}
  ${srv_files}
)

ament_export_dependencies(rosidl_default_runtime)

if(BUILD_TESTING)
  find_package(ament_lint_auto REQUIRED)
  set(ament_cmake_copyright_FOUND TRUE)
  set(ament_cmake_cpplint_FOUND TRUE)
  ament_lint_auto_find_test_dependencies()
endif()

ament_package()
`
}

func genInterfacesPkgXML() string {
	return `<?xml version="1.0"?>
<?xml-model href="http://download.ros.org/schema/package_format3.xsd" schematypens="http://www.w3.org/2001/XMLSchema"?>
<package format="3">
  <name>{{NAME}}_interfaces</name>
  <version>0.0.0</version>
  <!-- TODO: Add a description for the interface package. -->
  <description>Custom ROS2 interfaces for the {{NAME}} component.</description>
  <maintainer email="todo@example.com">TODO</maintainer>
  <license>TODO</license>

  <buildtool_depend>ament_cmake</buildtool_depend>
  <buildtool_depend>rosidl_default_generators</buildtool_depend>

  <exec_depend>rosidl_default_runtime</exec_depend>
  <member_of_group>rosidl_interface_packages</member_of_group>

  <test_depend>ament_lint_auto</test_depend>
  <test_depend>ament_lint_common</test_depend>

  <export>
    <build_type>ament_cmake</build_type>
  </export>
</package>
`
}

func genPingSrv() string {
	return `# TODO: Replace this service definition with one that fits your component.
#
# The {{NAME}} node advertises this service on ~/ping, which resolves to
# /{{NAME}}_node/ping (or /<agent>/{{NAME}}_node/ping when namespaced).
# The Ping behavior calls this service and exposes the reply as a BT output port.

# Request
string message

---

# Response
bool success
string reply
`
}

// ---------------------------------------------------------------------------
// {{NAME}} node package
// ---------------------------------------------------------------------------

func genNodeCMake() string {
	return `cmake_minimum_required(VERSION 3.8)
project({{NAME}})

if(CMAKE_COMPILER_IS_GNUCXX OR CMAKE_CXX_COMPILER_ID MATCHES "Clang")
  add_compile_options(-Wall -Wextra -Wpedantic)
endif()

set(CMAKE_CXX_STANDARD 20)

find_package(ament_cmake REQUIRED)
find_package(rclcpp REQUIRED)
find_package({{NAME}}_interfaces REQUIRED)

# TODO: Add find_package() calls for any additional ROS2 packages your node uses.

include_directories(include)

add_executable({{NAME}}_node
  src/{{NAME}}_node.cpp
)

ament_target_dependencies({{NAME}}_node
  rclcpp
  {{NAME}}_interfaces
  # TODO: Add additional ament dependencies here.
)

install(TARGETS {{NAME}}_node
  DESTINATION lib/${PROJECT_NAME}
)

if(BUILD_TESTING)
  find_package(ament_lint_auto REQUIRED)
  set(ament_cmake_copyright_FOUND TRUE)
  set(ament_cmake_cpplint_FOUND TRUE)
  ament_lint_auto_find_test_dependencies()
endif()

ament_package()
`
}

func genNodePkgXML() string {
	return `<?xml version="1.0"?>
<?xml-model href="http://download.ros.org/schema/package_format3.xsd" schematypens="http://www.w3.org/2001/XMLSchema"?>
<package format="3">
  <name>{{NAME}}</name>
  <version>0.0.0</version>
  <!-- TODO: Add a description for the node package. -->
  <description>ROS2 node package for the {{NAME}} component.</description>
  <maintainer email="todo@example.com">TODO</maintainer>
  <license>TODO</license>

  <buildtool_depend>ament_cmake</buildtool_depend>

  <depend>rclcpp</depend>
  <depend>{{NAME}}_interfaces</depend>
  <!-- TODO: Add additional <depend> entries for packages your node uses. -->

  <test_depend>ament_lint_auto</test_depend>
  <test_depend>ament_lint_common</test_depend>

  <export>
    <build_type>ament_cmake</build_type>
  </export>
</package>
`
}

func genNodeHpp() string {
	return `#pragma once

#include <string>
#include <rclcpp/rclcpp.hpp>
#include "{{NAME}}_interfaces/srv/ping.hpp"

// TODO: Add additional #includes for any message/service types your node uses.

namespace {{NAME}} {

/**
 * {{CLASS}}Node — the main ROS2 node for the {{NAME}} component.
 *
 * It advertises a single Ping service (~/ping) that echoes back the request
 * message prefixed with a configurable string. Replace this with whatever
 * your component actually needs to do.
 */
class {{CLASS}}Node : public rclcpp::Node
{
public:
    explicit {{CLASS}}Node(const rclcpp::NodeOptions & opts);

private:
    void handlePing(
        const std::shared_ptr<{{NAME}}_interfaces::srv::Ping::Request> req,
        std::shared_ptr<{{NAME}}_interfaces::srv::Ping::Response> res);

    // TODO: Add member variables for subscriptions, publishers, timers, etc.
    rclcpp::Service<{{NAME}}_interfaces::srv::Ping>::SharedPtr ping_service_;
    std::string prefix_;
};

}  // namespace {{NAME}}
`
}

func genNodeCpp() string {
	return `#include "{{NAME}}/{{NAME}}_node.hpp"

namespace {{NAME}} {

{{CLASS}}Node::{{CLASS}}Node(const rclcpp::NodeOptions & opts)
: rclcpp::Node("{{NAME}}_node", opts)
{
    // TODO: Replace the 'prefix' parameter with parameters your node actually needs.
    // declare_parameter<T>(name, default) is the simplest way to expose parameters.
    // For larger parameter sets consider using the generate_parameter_library package.
    prefix_ = this->declare_parameter<std::string>("prefix", "Pong");

    // Advertise the Ping service.
    // ~/ping resolves to /{{NAME}}_node/ping (or /<agent>/{{NAME}}_node/ping when namespaced).
    ping_service_ = this->create_service<{{NAME}}_interfaces::srv::Ping>(
        "~/ping",
        std::bind(&{{CLASS}}Node::handlePing, this,
                  std::placeholders::_1, std::placeholders::_2));

    RCLCPP_INFO(this->get_logger(),
        "{{CLASS}}Node started. Prefix: '%s'", prefix_.c_str());

    // TODO: Create subscriptions, publishers, timers, action servers, etc. here.
}

void {{CLASS}}Node::handlePing(
    const std::shared_ptr<{{NAME}}_interfaces::srv::Ping::Request> req,
    std::shared_ptr<{{NAME}}_interfaces::srv::Ping::Response> res)
{
    // TODO: Replace with the actual logic your service needs to perform.
    res->reply   = prefix_ + ": " + req->message;
    res->success = true;

    RCLCPP_INFO(this->get_logger(),
        "Ping received: '%s'  ->  '%s'",
        req->message.c_str(), res->reply.c_str());
}

}  // namespace {{NAME}}

int main(int argc, char ** argv)
{
    rclcpp::init(argc, argv);
    auto node = std::make_shared<{{NAME}}::{{CLASS}}Node>(rclcpp::NodeOptions());
    rclcpp::spin(node);
    rclcpp::shutdown();
    return 0;
}
`
}

// ---------------------------------------------------------------------------
// {{NAME}}_behaviors package
// ---------------------------------------------------------------------------

func genBehaviorsCMake() string {
	return `cmake_minimum_required(VERSION 3.8)
project({{NAME}}_behaviors)

if(CMAKE_COMPILER_IS_GNUCXX OR CMAKE_CXX_COMPILER_ID MATCHES "Clang")
  add_compile_options(-Wall -Wextra -Wpedantic)
endif()

set(CMAKE_CXX_STANDARD 20)

# BUILD_SHARED_LIBS is OFF by default (static build used during the base stage).
# The Dockerfile's export stage passes -DBUILD_SHARED_LIBS=ON to produce the
# .so plugin that the Coral CLI copies into /coral_lib/behaviors/.
option(BUILD_SHARED_LIBS "Build shared libraries instead of static" OFF)

find_package(ament_cmake REQUIRED)
find_package(rclcpp REQUIRED)
find_package(behaviortree_cpp REQUIRED)
find_package({{NAME}}_interfaces REQUIRED)

# TODO: Add find_package() calls for any additional dependencies your behaviors need.

include_directories(include)

set(BEHAVIOR_DEPS
  rclcpp
  behaviortree_cpp
  {{NAME}}_interfaces
  # IMPORTANT: Only dependencies available within the downstream executor can be used here. The behavior plugin is loaded dynamically at runtime and must not have any unavailable dependencies. 
)

set(LIBRARY_TYPE STATIC)
if(BUILD_SHARED_LIBS)
  set(LIBRARY_TYPE SHARED)
endif()

# TODO: Add additional source files here as you implement more behaviors.
add_library({{NAME}}_behaviors ${LIBRARY_TYPE}
  src/ping.cpp
  src/plugin.cpp
)
target_compile_definitions({{NAME}}_behaviors PRIVATE BUILD_${LIBRARY_TYPE}_LIBRARY)
ament_target_dependencies({{NAME}}_behaviors ${BEHAVIOR_DEPS})

ament_export_targets(${PROJECT_NAME}Targets HAS_LIBRARY_TARGET)
ament_export_dependencies(${BEHAVIOR_DEPS})

install(
  DIRECTORY include/
  DESTINATION include
)

install(
  TARGETS ${PROJECT_NAME}
  EXPORT ${PROJECT_NAME}Targets
  LIBRARY DESTINATION lib
  ARCHIVE DESTINATION lib
  RUNTIME DESTINATION bin
  INCLUDES DESTINATION include
)

if(BUILD_TESTING)
  find_package(ament_lint_auto REQUIRED)
  set(ament_cmake_copyright_FOUND TRUE)
  set(ament_cmake_cpplint_FOUND TRUE)
  ament_lint_auto_find_test_dependencies()
endif()

ament_package()
`
}

func genBehaviorsPkgXML() string {
	return `<?xml version="1.0"?>
<?xml-model href="http://download.ros.org/schema/package_format3.xsd" schematypens="http://www.w3.org/2001/XMLSchema"?>
<package format="3">
  <name>{{NAME}}_behaviors</name>
  <version>0.0.0</version>
  <!-- TODO: Add a description for the behaviors package. -->
  <description>BehaviorTree.CPP plugin package for the {{NAME}} component.</description>
  <maintainer email="todo@example.com">TODO</maintainer>
  <license>TODO</license>

  <buildtool_depend>ament_cmake</buildtool_depend>

  <depend>rclcpp</depend>
  <depend>behaviortree_cpp</depend>
  <depend>{{NAME}}_interfaces</depend>
  <!-- TODO: Add additional <depend> entries for packages your behaviors use. -->

  <test_depend>ament_lint_auto</test_depend>
  <test_depend>ament_lint_common</test_depend>

  <export>
    <build_type>ament_cmake</build_type>
  </export>
</package>
`
}

func genPingHpp() string {
	return `#pragma once

// TODO: Rename this file and class to something meaningful for your component.
//
// This is a BehaviorTree.CPP StatefulActionNode. The three lifecycle methods are:
//   onStart()   — called once when the node is ticked for the first time
//   onRunning() — called on every subsequent tick while the node returns RUNNING
//   onHalted()  — called if the tree is halted before the node finishes
//
// Use StatefulActionNode for anything asynchronous (service calls, actions, timers).
// Use SyncActionNode for simple synchronous work that completes in a single tick.

#include <memory>
#include <optional>
#include <rclcpp/rclcpp.hpp>
#include <behaviortree_cpp/action_node.h>
#include "{{NAME}}_interfaces/srv/ping.hpp"

namespace {{NAME}}_behaviors {

class Ping : public BT::StatefulActionNode
{
public:
    using PingSrv = {{NAME}}_interfaces::srv::Ping;

    Ping(const std::string & name, const BT::NodeConfig & config);

    // Declare the input and output ports that this node exposes to the behavior tree.
    // Ports are the typed, named "wires" that connect nodes through the blackboard.
    static BT::PortsList providedPorts();

    BT::NodeStatus onStart() override;
    BT::NodeStatus onRunning() override;
    void           onHalted() override;

private:
    // Each behavior node creates its own rclcpp::Node for service/action clients.
    // This keeps nodes decoupled and avoids callback executor conflicts.
    std::shared_ptr<rclcpp::Node>                            node_;
    rclcpp::Client<PingSrv>::SharedPtr                       client_;
    std::optional<rclcpp::Client<PingSrv>::FutureAndRequestId> future_;
    rclcpp::Time                                             stamp_;
    int                                                      timeout_;
};

}  // namespace {{NAME}}_behaviors
`
}

func genPingCpp() string {
	return `#include "{{NAME}}_behaviors/ping.hpp"

// TODO: Replace this behavior with one that matches your component's service.
//
// The full call chain is:
//   BT executor ticks Ping::onStart()
//     → creates a service client and sends an async request to {{NAME}}_node/ping
//     → returns RUNNING immediately (non-blocking)
//   BT executor ticks Ping::onRunning() each cycle
//     → spins the future for up to 5 ms
//     → returns RUNNING until the response arrives or the timeout expires
//   On success: writes the reply to the 'reply' output port and returns SUCCESS
//   On failure / timeout: returns FAILURE

namespace {{NAME}}_behaviors {

Ping::Ping(const std::string & name, const BT::NodeConfig & config)
: BT::StatefulActionNode(name, config),
  // Each behavior node owns its own rclcpp::Node. The node name matches the BT
  // node name so it appears meaningfully in 'ros2 node list'.
  node_(rclcpp::Node::make_shared(name))
{}

BT::PortsList Ping::providedPorts()
{
    // TODO: Replace these ports with the inputs and outputs your behavior needs.
    return {
        // InputPort<T>(key, default, description) — reads from the BT blackboard.
        BT::InputPort<std::string>(
            "agent", "",
            "Namespace of the agent running {{NAME}}. "
            "Leave empty to call in the local namespace."),
        BT::InputPort<std::string>(
            "message", "hello",
            "Message to send to the Ping service."),
        BT::InputPort<int>(
            "timeout", 5,
            "Maximum seconds to wait for the service response before returning FAILURE."),
        // OutputPort<T>(key, description) — writes to the BT blackboard.
        BT::OutputPort<std::string>(
            "reply",
            "Reply string returned by the Ping service."),
    };
}

BT::NodeStatus Ping::onStart()
{
    // Build the service path.
    // ~/ping on the node resolves to /{{NAME}}_node/ping (absolute).
    // When the component runs under an agent namespace the full path becomes
    // /<agent>/{{NAME}}_node/ping.
    const std::string service_handle{"{{NAME}}_node/ping"};

    auto agent = getInput<std::string>("agent");
    if (agent.has_value() && !agent->empty()) {
        client_ = node_->create_client<PingSrv>("/" + agent.value() + "/" + service_handle);
    } else {
        client_ = node_->create_client<PingSrv>(service_handle);
    }

    auto req     = std::make_shared<PingSrv::Request>();
    req->message = getInput<std::string>("message").value_or("hello");
    timeout_     = getInput<int>("timeout").value_or(5);

    future_ = client_->async_send_request(req);
    stamp_  = node_->now();

    return BT::NodeStatus::RUNNING;
}

BT::NodeStatus Ping::onRunning()
{
    if (!future_.has_value()) {
        RCLCPP_ERROR(node_->get_logger(),
            "Ping::onRunning called without an active future — this should never happen.");
        return BT::NodeStatus::FAILURE;
    }

    // Poll the future for up to 5 ms so the BT executor stays responsive.
    auto status = rclcpp::spin_until_future_complete(
        node_->get_node_base_interface(), future_.value(),
        std::chrono::milliseconds(5));

    switch (status) {
        case rclcpp::FutureReturnCode::TIMEOUT: {
            const rclcpp::Duration elapsed = node_->now() - stamp_;
            if (elapsed > rclcpp::Duration(std::chrono::seconds(timeout_))) {
                RCLCPP_ERROR(node_->get_logger(),
                    "Ping timed out after %d second(s).", timeout_);
                client_->remove_pending_request(future_.value());
                return BT::NodeStatus::FAILURE;
            }
            return BT::NodeStatus::RUNNING;
        }
        case rclcpp::FutureReturnCode::INTERRUPTED:
            RCLCPP_ERROR(node_->get_logger(), "Ping request was interrupted.");
            return BT::NodeStatus::FAILURE;

        case rclcpp::FutureReturnCode::SUCCESS: {
            auto resp = future_->get();
            setOutput<std::string>("reply", resp->reply);
            RCLCPP_INFO(node_->get_logger(),
                "Ping complete. Reply: '%s'", resp->reply.c_str());
            return resp->success ? BT::NodeStatus::SUCCESS : BT::NodeStatus::FAILURE;
        }
    }
    return BT::NodeStatus::FAILURE;
}

void Ping::onHalted()
{
    if (future_.has_value()) {
        client_->remove_pending_request(future_.value());
    }
}

}  // namespace {{NAME}}_behaviors
`
}

func genPluginCpp() string {
	return `#include <behaviortree_cpp/bt_factory.h>
#include "{{NAME}}_behaviors/ping.hpp"

// TODO: Register additional behavior nodes here as you add them.
//
// BT_RegisterNodesFromPlugin is the required entry point for BehaviorTree.CPP
// shared library plugins. The Coral executor calls this when it loads the .so.

extern "C" void BT_RegisterNodesFromPlugin(BT::BehaviorTreeFactory & factory)
{
    factory.registerNodeType<{{NAME}}_behaviors::Ping>("Ping");

    // Metadata appears in the BT.CPP Groot visualizer and in 'coral inspect'.
    factory.addMetadataToManifest("Ping", {
        // TODO: Update the description to reflect what your component does.
        {"description", "Calls the {{NAME}} Ping service and returns the reply."},
    });
}
`
}
