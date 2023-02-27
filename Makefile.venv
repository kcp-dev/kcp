#
# SEAMLESSLY MANAGE PYTHON VIRTUAL ENVIRONMENT WITH A MAKEFILE
#
# https://github.com/sio/Makefile.venv             v2022.07.20
#
#
# Insert `include Makefile.venv` at the bottom of your Makefile to enable these
# rules.
#
# When writing your Makefile use '$(VENV)/python' to refer to the Python
# interpreter within virtual environment and '$(VENV)/executablename' for any
# other executable in venv.
#
# This Makefile provides the following targets:
#   venv
#       Use this as a dependency for any target that requires virtual
#       environment to be created and configured
#   python, ipython
#       Use these to launch interactive Python shell within virtual environment
#   shell, bash, zsh
#       Launch interactive command line shell. "shell" target launches the
#       default shell Makefile executes its rules in (usually /bin/sh).
#       "bash" and "zsh" can be used to refer to the specific desired shell.
#   show-venv
#       Show versions of Python and pip, and the path to the virtual environment
#   clean-venv
#       Remove virtual environment
#   $(VENV)/executable_name
#       Install `executable_name` with pip. Only packages with names matching
#       the name of the corresponding executable are supported.
#       Use this as a lightweight mechanism for development dependencies
#       tracking. E.g. for one-off tools that are not required in every
#       developer's environment, therefore are not included into
#       requirements.txt or setup.py.
#       Note:
#           Rules using such target or dependency MUST be defined below
#           `include` directive to make use of correct $(VENV) value.
#       Example:
#           codestyle: $(VENV)/pyflakes
#               $(VENV)/pyflakes .
#       See `ipython` target below for another example.
#
# This Makefile can be configured via following variables:
#   PY
#       Command name for system Python interpreter. It is used only initially to
#       create the virtual environment
#       Default: python3
#   REQUIREMENTS_TXT
#       Space separated list of paths to requirements.txt files.
#       Paths are resolved relative to current working directory.
#       Default: requirements.txt
#
#       Non-existent files are treated as hard dependencies,
#       recipes for creating such files must be provided by the main Makefile.
#       Providing empty value (REQUIREMENTS_TXT=) turns off processing of
#       requirements.txt even when the file exists.
#   SETUP_PY
#       Space separated list of paths to setup.py files.
#       Corresponding packages will be installed into venv in editable mode
#       along with all their dependencies
#       Default: setup.py
#
#       Non-existent and empty values are treated in the same way as for REQUIREMENTS_TXT.
#   WORKDIR
#       Parent directory for the virtual environment.
#       Default: current working directory.
#   VENVDIR
#       Python virtual environment directory.
#       Default: $(WORKDIR)/.venv
#
# This Makefile was written for GNU Make and may not work with other make
# implementations.
#
#
# Copyright (c) 2019-2020 Vitaly Potyarkin
#
# Licensed under the Apache License, Version 2.0
#    <http://www.apache.org/licenses/LICENSE-2.0>
#


#
# Configuration variables
#

WORKDIR?=.
VENVDIR?=$(WORKDIR)/.venv
REQUIREMENTS_TXT?=$(wildcard requirements.txt)  # Multiple paths are supported (space separated)
SETUP_PY?=$(wildcard setup.py)                  # Multiple paths are supported (space separated)
SETUP_CFG?=$(foreach s,$(SETUP_PY),$(wildcard $(patsubst %setup.py,%setup.cfg,$(s))))
MARKER=.initialized-with-Makefile.venv


#
# Python interpreter detection
#

_PY_AUTODETECT_MSG=Detected Python interpreter: $(PY). Use PY environment variable to override

ifeq (ok,$(shell test -e /dev/null 2>&1 && echo ok))
NULL_STDERR=2>/dev/null
else
NULL_STDERR=2>NUL
endif

ifndef PY
_PY_OPTION:=python3
ifeq (ok,$(shell $(_PY_OPTION) -c "print('ok')" $(NULL_STDERR)))
PY=$(_PY_OPTION)
endif
endif

ifndef PY
_PY_OPTION:=$(VENVDIR)/bin/python
ifeq (ok,$(shell $(_PY_OPTION) -c "print('ok')" $(NULL_STDERR)))
PY=$(_PY_OPTION)
$(info $(_PY_AUTODETECT_MSG))
endif
endif

ifndef PY
_PY_OPTION:=$(subst /,\,$(VENVDIR)/Scripts/python)
ifeq (ok,$(shell $(_PY_OPTION) -c "print('ok')" $(NULL_STDERR)))
PY=$(_PY_OPTION)
$(info $(_PY_AUTODETECT_MSG))
endif
endif

ifndef PY
_PY_OPTION:=py -3
ifeq (ok,$(shell $(_PY_OPTION) -c "print('ok')" $(NULL_STDERR)))
PY=$(_PY_OPTION)
$(info $(_PY_AUTODETECT_MSG))
endif
endif

ifndef PY
_PY_OPTION:=python
ifeq (ok,$(shell $(_PY_OPTION) -c "print('ok')" $(NULL_STDERR)))
PY=$(_PY_OPTION)
$(info $(_PY_AUTODETECT_MSG))
endif
endif

ifndef PY
define _PY_AUTODETECT_ERR
Could not detect Python interpreter automatically.
Please specify path to interpreter via PY environment variable.
endef
$(error $(_PY_AUTODETECT_ERR))
endif


#
# Internal variable resolution
#

VENV=$(VENVDIR)/bin
EXE=
# Detect windows
ifeq (win32,$(shell $(PY) -c "import __future__, sys; print(sys.platform)"))
VENV=$(VENVDIR)/Scripts
EXE=.exe
endif

touch=touch $(1)
ifeq (,$(shell command -v touch $(NULL_STDERR)))
# https://ss64.com/nt/touch.html
touch=type nul >> $(subst /,\,$(1)) && copy /y /b $(subst /,\,$(1))+,, $(subst /,\,$(1))
endif

RM?=rm -f
ifeq (,$(shell command -v $(firstword $(RM)) $(NULL_STDERR)))
RMDIR:=rd /s /q
else
RMDIR:=$(RM) -r
endif


#
# Virtual environment
#

.PHONY: venv
venv: $(VENV)/$(MARKER)

.PHONY: clean-venv
clean-venv:
	-$(RMDIR) "$(VENVDIR)"

.PHONY: show-venv
show-venv: venv
	@$(VENV)/python -c "import sys; print('Python ' + sys.version.replace('\n',''))"
	@$(VENV)/pip --version
	@echo venv: $(VENVDIR)

.PHONY: debug-venv
debug-venv:
	@echo "PATH (Shell)=$$PATH"
	@$(MAKE) --version
	$(info PATH (GNU Make)="$(PATH)")
	$(info SHELL="$(SHELL)")
	$(info PY="$(PY)")
	$(info REQUIREMENTS_TXT="$(REQUIREMENTS_TXT)")
	$(info SETUP_PY="$(SETUP_PY)")
	$(info SETUP_CFG="$(SETUP_CFG)")
	$(info VENVDIR="$(VENVDIR)")
	$(info VENVDEPENDS="$(VENVDEPENDS)")
	$(info WORKDIR="$(WORKDIR)")


#
# Dependencies
#

ifneq ($(strip $(REQUIREMENTS_TXT)),)
VENVDEPENDS+=$(REQUIREMENTS_TXT)
endif

ifneq ($(strip $(SETUP_PY)),)
VENVDEPENDS+=$(SETUP_PY)
endif
ifneq ($(strip $(SETUP_CFG)),)
VENVDEPENDS+=$(SETUP_CFG)
endif

$(VENV):
	$(PY) -m venv $(VENVDIR)
	$(VENV)/python -m pip install --upgrade pip setuptools wheel

$(VENV)/$(MARKER): $(VENVDEPENDS) | $(VENV)
ifneq ($(strip $(REQUIREMENTS_TXT)),)
	$(VENV)/pip install $(foreach path,$(REQUIREMENTS_TXT),-r $(path))
endif
ifneq ($(strip $(SETUP_PY)),)
	$(VENV)/pip install $(foreach path,$(SETUP_PY),-e $(dir $(path)))
endif
	$(call touch,$(VENV)/$(MARKER))


#
# Interactive shells
#

.PHONY: python
python: venv
	exec $(VENV)/python

.PHONY: ipython
ipython: $(VENV)/ipython
	exec $(VENV)/ipython

.PHONY: shell
shell: venv
	. $(VENV)/activate && exec $(notdir $(SHELL))

.PHONY: bash zsh
bash zsh: venv
	. $(VENV)/activate && exec $@


#
# Commandline tools (wildcard rule, executable name must match package name)
#

ifneq ($(EXE),)
$(VENV)/%: $(VENV)/%$(EXE) ;
.PHONY:    $(VENV)/%
.PRECIOUS: $(VENV)/%$(EXE)
endif

$(VENV)/%$(EXE): $(VENV)/$(MARKER)
	$(VENV)/pip install --upgrade $*
	$(call touch,$@)
