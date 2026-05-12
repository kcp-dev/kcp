#!/usr/bin/env bash

# Copyright 2026 The kcp Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.


# suite is a simple helper script to execute load tests and capture their logs in a consistent and resilient way.
# It saves the logs to a file and outputs them on your screen. You can detach from the logs with Ctrl+C, while the test keeps running in the background.
#
# It can also be used in scripting/CI as well, as it exits once the go test has finished.
# Additionally it works with jumphosts in case the ssh connection terminates.

set -euo pipefail

# set to a fixed timezone so logfile names are consistent
export TZ="Europe/Berlin"
logdir=".loadtest-results"
mkdir -p "$logdir"

# parse user input
testname=${1:-}
if [[ -z "$testname" ]]; then
  echo "Usage: $0 <testname>"
  exit 1
fi

# execute the test and capture logs
logfile="$logdir/${testname}_$(date '+%Y-%m-%d-%H:%M:%S').log"
echo "Executing test: $testname"
echo "Logs will be saved to: $logfile"

go test -timeout 2h -test.fullpath=true -run "^${testname}$" -count=1 -v github.com/kcp-dev/kcp/test/load/testing &> "$logfile" &
test_pid=$!

# on Ctrl+C: stop tailing but keep the test running
stop_tailing() {
  echo ""
  echo "Detached from output. Test (PID $test_pid) is still running. Kill it manually if required."
  echo "Resume with: tail -f $logfile"
  exit 0
}
trap stop_tailing INT TERM

# stream logs until the test process exits
tail -f --pid="$test_pid" "$logfile"

wait "$test_pid"
exit_code=$?

exit "$exit_code"
