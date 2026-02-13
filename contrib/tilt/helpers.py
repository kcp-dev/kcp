# Copyright 2026 The KCP Authors.
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

def kcp_build(exe):
    local_resource(
        'build '+exe,
        cmd='go build -o ./bin/{exe} ../../cmd/{exe}'.format(exe = exe),
        deps = [
            '../../cmd',
            '../../pkg',
            '../../staging',
            ],
        labels=['kcp'],
        allow_parallel=True,
    )
    docker_build(
        ref=exe,
        context='./bin',
        dockerfile='Dockerfile',
        build_args={'BIN': exe},
        live_update=[
            sync('./bin/'+exe, '/'+exe),
        ],
    )
