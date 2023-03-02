# Copyright 2023 The KCP Authors.
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

import copy

from mkdocs_macros import fix_url


def define_env(env):
    """
    This is the hook for defining variables, macros, and filters. See
    https://mkdocs-macros-plugin.readthedocs.io/en/latest/macros/#the-define_env-function for more details.

    :param env: the Jinja2 environment
    """

    @env.macro
    def section_items(page, nav, config):
        """
        Returns a list of all pages that are siblings to page.

        :param page: the current page. This will typically be an index.md page.
        :param nav: the mkdocs navigation object.
        :param config: the mkdocs config object.
        :return: a list of all the sibling pages.
        """

        if page.parent:
            children = page.parent.children
        else:
            children = nav.items

        siblings = []
        for child in children:
            if child is page:
                # don't include the passed in page in the list
                continue
            if child.is_section:
                # don't include sections
                continue
            if child.file.name == 'index':
                # don't include index pages
                continue

            # Because some pages might not have been loaded yet, we have to do so now, to get title/metadata.
            child.read_source(config)

            # Copy so we don't modify the original
            child = copy.deepcopy(child)

            # Have to fix the URL - see
            # https://mkdocs-macros-plugin.readthedocs.io/en/latest/tips/#how-do-i-deal-with-relative-links-to-documentsimages
            child.file.url = fix_url(child.url)

            siblings.append(child)

        return siblings
