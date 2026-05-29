"""Module for running programming assignments using nsjail"""

import logging
import os

import subprocess32 as subprocess
from google.protobuf.text_format import MessageToString

from proto import request_pb2


class NsJail:
    """Class storing the nsjail path"""

    @classmethod
    def run_command(cls, args, input_file=None):
        """
        Runs a command using `args` and returns the output written to
        stdout, stderr
        """
        if not args:
            return None, None
        if input_file:
            sp = subprocess.Popen(
                [str(arg) for arg in args],
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                stdin=input_file,
            )
        else:
            sp = subprocess.Popen(
                [str(arg) for arg in args],
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
            )
        out, err = sp.communicate()
        return out, err


class CodeRunner(NsJail):
    """Class which runs Programs"""

    @classmethod
    def parse_arg(cls, arg, template):
        """Parses the arg for keywords such as FILENAME"""
        # TODO(rthakker) verify this works
        for key, value in template.items():
            arg = arg.replace(f"{{{{ {key} }}}}", str(value))
        return arg

    @classmethod
    def parse_error(cls, err):
        """
        Parses the output by nsjail and returns a human readable error code
        and a CodeReply Status.
        """
        # TODO(rthakker) Add more error types
        if isinstance(err, bytes):
            err = err.decode("utf-8", "ignore")
        if "run time >= time limit" in err:
            status = request_pb2.CodeReply.TIME_LIMIT_EXCEEDED
        elif "terminated with signal" in err:
            # TODO(rthakker) Write a better check for error code
            # to distinguish between memory limit and runtime error.
            status = request_pb2.CodeReply.RUNTIME_ERROR
        elif "exited with status: 0" in err:
            status = request_pb2.CodeReply.WRONG_ANSWER
        else:
            status = request_pb2.CodeReply.RUNTIME_ERROR
        return status

    @classmethod
    def extract_errors(cls, raw_err):
        """
        If the nsjail output and program output are in the same string, this
        returns both as two separate objects.
        """
        if isinstance(raw_err, bytes):  # Ensure raw_err is a string
            raw_err = raw_err.decode("utf-8", "ignore")

        nsjail_output, error = [], []

        for line in raw_err.split("\n"):
            if line:
                if line[0] == "[":
                    nsjail_output.append(line)
                else:
                    error.append(line)

        return "\n".join(nsjail_output), "\n".join(error)

    def __init__(
        self,
        request_obj,
        root_directory,
        uid,
        public_settings,
        private_settings,
    ):
        """Initializes the parameters required to run a program"""
        # TODO(rthakker) write docstrings for compilation and run options
        super().__init__()
        self.request_obj = request_obj
        self.root_directory = root_directory
        self.uid = uid
        self.private_settings = private_settings
        self.public_settings = public_settings

    def compile(self, reply_obj):
        """
        Compiles the program

        reply_obj: Should be a request_pb2.CodeReply.CompilationResult object
        """

        # Get default settings for this language
        language_settings = [
            language_settings
            for language_settings in self.public_settings.language_settings
            if language_settings.language == self.request_obj.language
        ][0]

        # Check if compilation is needed
        if not language_settings.HasField("compilation_options"):
            # No need to compile
            # TODO(rthakker) use NOT_RUN instead of OK
            reply_obj.status = request_pb2.CodeReply.OK
            return

        language_independent_args = ""
        if self.private_settings.default_compilation_settings.nsjail_args:
            language_independent_args += (
                self.private_settings.default_compilation_settings.nsjail_args
            )
        if self.private_settings.default_common_settings.nsjail_args:
            language_independent_args += " " + (
                self.private_settings.default_common_settings.nsjail_args
            )
        if self.public_settings.default_compilation_settings.nsjail_args:
            language_independent_args += " " + (
                self.public_settings.default_compilation_settings.nsjail_args
            )
        if self.public_settings.default_common_settings.nsjail_args:
            language_independent_args += " " + (
                self.public_settings.default_common_settings.nsjail_args
            )

        # Update resource limits from request
        resource_limits = language_settings.compilation_options.resource_limits
        if self.request_obj.HasField("compilation_options"):
            resource_limits.MergeFrom(
                self.request_obj.compilation_options.resource_limits,
            )

        language_independent_args = self.parse_arg(
            arg=language_independent_args,
            template={
                "ROOT_DIRECTORY": self.root_directory,
                "USER_ID": self.uid,
                "GROUP_ID": self.uid,
                "TIME_LIMIT": (resource_limits.time_limit),
                "MEMORY_LIMIT": (resource_limits.memory_limit),
                "PROCESS_LIMIT": (resource_limits.process_limit),
                "LOG_PATH": os.path.join(self.root_directory, "log").strip(),
            },
        )

        extra_args = ""
        if self.request_obj.HasField("compilation_options"):
            extra_args = " ".join(
                self.request_obj.compilation_options.extra_args,
            )

        language_dependent_args = (
            language_settings.compilation_options.path
            + " "
            + self.parse_arg(
                arg=language_settings.compilation_options.args,
                template={
                    "EXTRA_ARGS": extra_args,
                    "FILENAME": language_settings.filename,
                },
            )
        )

        args = [
            self.parse_arg(
                arg=self.public_settings.nsjail_path,
                template=os.environ,
            ),
        ]
        args.extend(
            self.parse_arg(
                arg=self.private_settings.nsjail_args,
                template={
                    "LANGUAGE_INDEPENDENT_ARGS": language_independent_args,
                    "LANGUAGE_DEPENDENT_ARGS": language_dependent_args,
                },
            ).split(),
        )
        # Run the command
        out, err = self.run_command(args)
        nsjail_output = open(os.path.join(self.root_directory, "log")).read()
        reply_obj.output = out
        reply_obj.error = err
        reply_obj.nsjail_output = nsjail_output
        if err:
            reply_obj.status = request_pb2.CodeReply.COMPILATION_ERROR
        else:
            reply_obj.status = request_pb2.CodeReply.OK

    def run(self, input_path, testcase_idx, log_path, reply_obj):
        """
        Runs the program and updates reply_obj with the results.

        reply_obj: Should be a reply_pb2.CodeReply.TestCaseResult object.
        """
        language_independent_args = ""
        if self.private_settings.default_runtime_settings.nsjail_args:
            language_independent_args += (
                self.private_settings.default_runtime_settings.nsjail_args
            )
        if self.private_settings.default_common_settings.nsjail_args:
            language_independent_args += " " + (
                self.private_settings.default_common_settings.nsjail_args
            )
        if self.public_settings.default_runtime_settings.nsjail_args:
            language_independent_args += " " + (
                self.public_settings.default_runtime_settings.nsjail_args
            )
        if self.public_settings.default_common_settings.nsjail_args:
            language_independent_args += " " + (
                self.public_settings.default_common_settings.nsjail_args
            )

        # Get default settings for this language
        language_settings = [
            language_settings
            for language_settings in self.public_settings.language_settings
            if language_settings.language == self.request_obj.language
        ][0]

        # Update resource limits from request
        resource_limits = language_settings.runtime_options.resource_limits
        if self.request_obj.HasField("runtime_options"):
            resource_limits.MergeFrom(
                self.request_obj.runtime_options.resource_limits,
            )

        language_independent_args = self.parse_arg(
            arg=language_independent_args,
            template={
                "ROOT_DIRECTORY": self.root_directory,
                "USER_ID": self.uid,
                "GROUP_ID": self.uid,
                "TIME_LIMIT": (resource_limits.time_limit),
                "MEMORY_LIMIT": (resource_limits.memory_limit),
                "PROCESS_LIMIT": (resource_limits.process_limit),
                "LOG_PATH": os.path.join(self.root_directory, "log").strip(),
            },
        )

        extra_args = ""
        if self.request_obj.HasField("runtime_options"):
            extra_args = " ".join(self.request_obj.runtime_options.extra_args)
        language_dependent_args = (
            language_settings.runtime_options.path
            + " "
            + self.parse_arg(
                arg=language_settings.runtime_options.args,
                template={
                    "EXTRA_ARGS": extra_args,
                    "FILENAME": language_settings.filename,
                    "BINARY_FILENAME": language_settings.binary_filename,
                },
            )
        )

        args = [
            self.parse_arg(
                arg=self.public_settings.nsjail_path,
                template=os.environ,
            ),
        ]
        args.extend(
            self.parse_arg(
                arg=self.private_settings.nsjail_args,
                template={
                    "LANGUAGE_INDEPENDENT_ARGS": language_independent_args,
                    "LANGUAGE_DEPENDENT_ARGS": language_dependent_args,
                },
            ).split(),
        )
        input_file = open(os.path.join(self.root_directory, input_path))
        logging.debug(str(args))

        out, raw_error = self.run_command(args, input_file=input_file)
        error = ""
        nsjail_output = ""

        expected_output = self.request_obj.testcases[
            testcase_idx
        ].output.encode("ascii", "ignore")

        if expected_output == out:
            status = request_pb2.CodeReply.OK
        elif expected_output.strip() == out.strip():
            status = request_pb2.CodeReply.PRESENTATION_ERROR
        else:
            # if os.path.isfile(
            #         os.path.join(self.root_directory, log_path).strip()):
            #     nsjail_output = open(
            #         os.path.join(self.root_directory, log_path)).read()
            #     error, status = self.parse_error(nsjail_output)
            # else:
            #     error = os.path.join(self.root_directory, log_path).strip()
            #     log('Failed to read nsjail log path: %s\n'
            #         'Raw error: %s' % (error, raw_error))
            #     nsjail_output = ''
            #     status = 5
            # TODO(rthakker) (repeat) Check why --log doesn't work
            status = self.parse_error(raw_error)
            nsjail_output, error = self.extract_errors(raw_error)
        reply_obj.actual_output = out
        reply_obj.status = status
        reply_obj.error = error
        reply_obj.nsjail_output = nsjail_output

    def run_using_pipe(self, input_path, output_path, log_path, reply_pipe):
        """
        Runs the program and sends the results to
        reply_pipe (when using multiprocessing)
        """
        reply_obj = request_pb2.CodeReply.TestCaseResult()

        self.run(
            input_path=input_path,
            output_path=output_path,
            log_path=log_path,
            reply_obj=reply_obj,
        )

        reply_pipe.send(MessageToString(reply_obj))
        reply_pipe.close()


class EvaluationScriptCodeRunner(CodeRunner):
    @classmethod
    def parse_error(cls, err):
        """
        Parses the output by nsjail and returns a human readable error code
        and a CodeReply Status.
        """
        if isinstance(err, bytes):
            err = err.decode("utf-8", "ignore")
        if "run time >= time limit" in err:
            status = request_pb2.CodeReply.TIME_LIMIT_EXCEEDED
        elif "terminated with signal" in err:
            status = request_pb2.CodeReply.RUNTIME_ERROR
        elif "exited with status: 0" in err:
            status = request_pb2.CodeReply.OK
        else:
            status = request_pb2.CodeReply.RUNTIME_ERROR
        return status

    def run(self, input_path, output_path, log_path, reply_obj):
        """
        Runs the program and updates reply_obj with the results.

        reply_obj: Should be a reply_pb2.CodeReply.TestCaseResult object.
        """
        language_independent_args = ""
        if self.private_settings.default_runtime_settings.nsjail_args:
            language_independent_args += (
                self.private_settings.default_runtime_settings.nsjail_args
            )
        if self.private_settings.default_common_settings.nsjail_args:
            language_independent_args += " " + (
                self.private_settings.default_common_settings.nsjail_args
            )
        if self.public_settings.default_runtime_settings.nsjail_args:
            language_independent_args += " " + (
                self.public_settings.default_runtime_settings.nsjail_args
            )
        if self.public_settings.default_common_settings.nsjail_args:
            language_independent_args += " " + (
                self.public_settings.default_common_settings.nsjail_args
            )

        # Get default settings for this language
        language_settings = [
            language_settings
            for language_settings in self.public_settings.language_settings
            if language_settings.language == self.request_obj.language
        ][0]

        # Update resource limits from request
        resource_limits = language_settings.runtime_options.resource_limits
        if self.request_obj.HasField("runtime_options"):
            resource_limits.MergeFrom(
                self.request_obj.runtime_options.resource_limits,
            )

        language_independent_args = self.parse_arg(
            arg=language_independent_args,
            template={
                "ROOT_DIRECTORY": self.root_directory,
                "USER_ID": self.uid,
                "GROUP_ID": self.uid,
                "TIME_LIMIT": (resource_limits.time_limit),
                "MEMORY_LIMIT": (resource_limits.memory_limit),
                "PROCESS_LIMIT": (resource_limits.process_limit),
                "LOG_PATH": os.path.join(self.root_directory, "log").strip(),
            },
        )

        extra_args = ""
        if self.request_obj.HasField("runtime_options"):
            extra_args = " ".join(self.request_obj.runtime_options.extra_args)
        language_dependent_args = (
            language_settings.runtime_options.path
            + " "
            + self.parse_arg(
                arg=language_settings.runtime_options.args,
                template={
                    "EXTRA_ARGS": extra_args,
                    "FILENAME": language_settings.filename,
                    "BINARY_FILENAME": language_settings.binary_filename,
                },
            )
        )

        args = [
            self.parse_arg(
                arg=self.public_settings.nsjail_path,
                template=os.environ,
            ),
        ]
        args.extend(
            self.parse_arg(
                arg=self.private_settings.nsjail_args,
                template={
                    "LANGUAGE_INDEPENDENT_ARGS": language_independent_args,
                    "LANGUAGE_DEPENDENT_ARGS": language_dependent_args,
                },
            ).split(),
        )

        # Evaluation scripts (e.g. SQL/DBMS) may need pg_virtualenv which
        # requires chown/setuid capabilities and no user namespace.
        # Inject these before the '--' separator to keep them scoped to
        # evaluation script execution only.
        pg_caps = [
            "--disable_clone_newuser",
            "--cap", "CAP_CHOWN",
            "--cap", "CAP_FOWNER",
            "--cap", "CAP_SETUID",
            "--cap", "CAP_SETGID",
            "--cap", "CAP_DAC_OVERRIDE",
        ]
        try:
            separator_idx = args.index("--")
            for i, cap in enumerate(pg_caps):
                args.insert(separator_idx + i, cap)
        except ValueError:
            args.extend(pg_caps)

        logging.debug(str(args))
        out, raw_error = self.run_command(args)
        reply_obj.evaluation_result_json = out
        reply_obj.nsjail_output = raw_error
        reply_obj.overall_status = self.parse_error(raw_error)
