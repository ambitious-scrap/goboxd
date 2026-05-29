"""
Module which overlooks running of one complete Code Question.
"""

import logging
import os
import random
from multiprocessing import Pipe, Process
from threading import Thread

from google.protobuf.text_format import Merge, MessageToString, ParseError

from base import Config
from code_evaluator.code_runner import CodeRunner, EvaluationScriptCodeRunner
from proto import default_settings_pb2, request_pb2


PRIVATE_SETTINGS = Merge(
    open("config/private_settings.conf").read(),
    default_settings_pb2.PrivateSettings(),
)

PUBLIC_SETTINGS = Merge(
    open("config/public_settings.conf").read(),
    default_settings_pb2.PublicSettings(),
)


class CodeManager:
    """Class for managing Programming Assignment execution"""

    @classmethod
    def update_public_settings(cls, public_settings_str):
        """
        Updates the global public_settings object and writes it
        to the respective file.
        """
        # TODO(rthakker) Remove this if it is of no use anymore
        global PUBLIC_SETTINGS
        try:
            PUBLIC_SETTINGS = Merge(
                public_settings_str,
                default_settings_pb2.PublicSettings(),
            )
            with open("config/public_settings.conf", "w") as outfile:
                outfile.write(MessageToString(PUBLIC_SETTINGS))
            return True
        except ParseError:
            logging.info("Failed to update public settings")
            return False

    def __init__(self, request, reply):
        """
        Initializes the CodeManager object.
        request should be a pb_request2.CodeRequest object, and
        reply shoul dbe a pb_request2.CodeReply object
        """
        # TODO(rthakker) Complete docstring above
        self.request = request
        self.code_runner = None
        self.uid = None
        self.reply = reply
        self.pipes = []
        self.processes = []
        self.threads = []

        # Initialize test case responses if required
        if len(self.reply.test_case_results) == 0:
            for _ in range(len(self.request.testcases)):
                self.reply.test_case_results.add(
                    status=request_pb2.CodeReply.NOT_RUN,
                )

    def generate_uid(self):
        """Generated a UID between 30000 and 60000"""
        self.uid = str(int(random.SystemRandom().random() * 30000 + 30000))

    def initialize_environment(self, attempt_no=1):
        """Initializes the sandboxed folder structure for this program to run"""
        if attempt_no > 3:
            return False

        # Make a copy to avoid overwriting PUBLIC SETTINGS while setting filename
        self.local_public_settings = default_settings_pb2.PublicSettings()
        self.local_public_settings.CopyFrom(PUBLIC_SETTINGS)
        self.generate_uid()
        # TODO(rthakker) See if making the UID visible is a good idea.
        self.root_directory = os.path.join(Config.HOME, f"nsjps_{self.uid}")
        if os.path.isdir(self.root_directory):
            # The rare case that this UID is already taken.
            # Since we're using the OS's random number generator, this is
            # very unlikely. However since we're converting to int,
            # Within a range of 30,000 numbers this *may* happen.
            # Hence just restart the initialization process.
            return self.initialize_environment()
        self.code_runner = CodeRunner(
            request_obj=self.request,
            root_directory=self.root_directory,
            uid=self.uid,
            public_settings=self.local_public_settings,
            private_settings=PRIVATE_SETTINGS,
        )
        return_code = os.system(f"mkdir -p {self.root_directory}/proc")
        if return_code != 0:
            self.cleanup()
            return self.initialize_environment(attempt_no=attempt_no + 1)
        # replace filenames if required
        # TODO(rthakker) See if you can use map instead of iterating
        # through lists

        language_settings = [
            language_settings
            for language_settings in self.local_public_settings.language_settings
            if language_settings.language == self.request.language
        ][0]

        if language_settings.filename == "TAKE_FROM_REQUEST":
            language_settings.filename = self.request.code.filename
        if language_settings.binary_filename == "TAKE_FROM_REQUEST":
            language_settings.binary_filename = (
                self.request.code.binary_filename
            )
        with open(
            os.path.join(self.root_directory, language_settings.filename),
            "w",
        ) as f:
            f.write(self.request.code.full_code)

        for i, testcase in enumerate(self.request.testcases):
            # Make directory called test_{{i}} in root_directory
            testcase.directory = os.path.join(
                self.root_directory,
                "test_%d" % i,
            )
            return_code = os.system(f"mkdir {testcase.directory}")
            if return_code != 0:
                self.cleanup()
                return self.initialize_environment(attempt_no=attempt_no + 1)
            with open(os.path.join(testcase.directory, "input"), "w") as f:
                f.write(testcase.input)
        return True

    def compile_program(self):
        """Compiles the program"""
        self.code_runner.compile(reply_obj=self.reply.compilation_result)
        return self.reply.compilation_result.status == request_pb2.CodeReply.OK

    def generate_kwargs(self, testcase_idx):
        """Generates kwargs to be passed to code_runner"""
        testcase = self.request.testcases[testcase_idx]
        testcase_dir = testcase.directory
        logging.info(str(self.request.runtime_options))
        testcase_result = self.reply.test_case_results[testcase_idx]
        kwargs = {
            "input_path": os.path.join(testcase_dir, "input"),
            "testcase_idx": testcase_idx,
            "log_path": os.path.join(testcase_dir, "log"),
            "reply_obj": testcase_result,
        }
        return kwargs

    def run_program(self, kwargs):
        """Runs the program against one testcase"""
        self.code_runner.run(**kwargs)

    def parse_result(self):
        pass

    def run_all(self):
        """
        Runs the program against all testcases.
        """
        if not self.initialize_environment():
            # TODO(rthakker) Add details in reply
            return
        if self.compile_program():
            for i in range(len(self.request.testcases)):
                self.run_program(self.generate_kwargs(i))
            self.parse_result()
        # Set overall status
        if self.reply.compilation_result.status == request_pb2.CodeReply.OK:
            self.reply.overall_status = request_pb2.CodeReply.OK
            for result in self.reply.test_case_results:
                if result.status != request_pb2.CodeReply.OK:
                    self.reply.overall_status = result.status
                    break
        else:
            self.reply.overall_status = request_pb2.CodeReply.COMPILATION_ERROR

    def cleanup(self):
        """Cleans up the created directory"""
        if self.root_directory and self.root_directory != "/":
            os.system(f"rm -rf {self.root_directory}")


class MultiprocessingCodeManager(CodeManager):
    """Class which runs testcases in separate processes"""

    def run_program(self, kwargs):
        pipe = Pipe()
        self.pipes.append(pipe)
        del kwargs["reply_obj"]
        kwargs["reply_pipe"] = pipe[1]
        process = Process(
            target=self.code_runner.run_using_pipe,
            kwargs=kwargs,
        )
        self.processes.append(process)
        process.start()

    def parse_result(self):
        """Reads and analyzes the results of all the testcase runs"""
        for i in range(len(self.request.testcases)):
            # Receive output from pipe
            result = self.pipes[i][0].recv()
            self.processes[i].join()
            self.pipes[i][0].close()
            # Merge the result
            Merge(result, self.reply.test_case_results[i])
            del result


class ThreadingCodeManager(CodeManager):
    """Class which runes testcases in separate threads"""

    def run_program(self, kwargs):
        thread = Thread(target=self.code_runner.run, kwargs=kwargs)
        self.threads.append(thread)
        thread.start()

    def parse_result(self):
        """Reads and analyzes the results of all the testcase runs"""
        # If using threads, no need to parse output as it would have
        # already been stored in reply_obj by now.
        # Just wait for threads to complete.
        for thread in self.threads:
            thread.join()


class EvaluationScriptCodeManager(ThreadingCodeManager):
    """Overloads ThreadingCodeManager to be used if evaluation_script is provided"""

    def initialize_environment(self, attempt_no=1):
        """Initializes the sandboxed folder structure for this program to run"""
        if attempt_no > 3:
            return False

        # Make a copy to avoid overwriting PUBLIC SETTINGS while setting filename
        self.local_public_settings = default_settings_pb2.PublicSettings()
        self.local_public_settings.CopyFrom(PUBLIC_SETTINGS)
        self.generate_uid()
        # TODO(rthakker) See if making the UID visible is a good idea.
        self.root_directory = os.path.join(Config.HOME, f"nsjps_{self.uid}")

        if os.path.isdir(self.root_directory):
            # The rare case that this UID is already taken.
            # Since we're using the OS's random number generator, this is
            # very unlikely. However since we're converting to int,
            # Within a range of 30,000 numbers this *may* happen.
            # Hence just restart the initialization process.
            return self.initialize_environment()
        self.code_runner = EvaluationScriptCodeRunner(
            request_obj=self.request,
            root_directory=self.root_directory,
            uid=self.uid,
            public_settings=self.local_public_settings,
            private_settings=PRIVATE_SETTINGS,
        )
        return_code = os.system(
            f"mkdir -m 777 -p {self.root_directory}/proc",
        )  # Make folder with 777
        if return_code != 0:
            self.cleanup()
            return self.initialize_environment(attempt_no=attempt_no + 1)

        language_settings = [
            language_settings
            for language_settings in self.local_public_settings.language_settings
            if language_settings.language == self.request.language
        ][0]
        # Swami : Alternative solution for above list comprehension could use filter/lambda
        #         HOWEVER List comprehension might still be a better approach! ( readable + possibly faster )
        # language_settings = filter(
        #                lambda language_settings: language_settings.language == self.request.language,
        #                self.local_public_settings.language_settings
        #                )[0]
        if language_settings.filename == "TAKE_FROM_REQUEST":
            language_settings.filename = self.request.code.filename
        if language_settings.binary_filename == "TAKE_FROM_REQUEST":
            language_settings.binary_filename = (
                self.request.code.binary_filename
            )

        with open(
            os.path.join(self.root_directory, language_settings.filename),
            "w",
            encoding="utf8",
        ) as f:
            f.write(self.request.code.full_code)

        if self.request.evaluation_script:
            with open(
                os.path.join(self.root_directory, "evaluator.script"),
                "w",
                encoding="utf8",
            ) as f:
                f.write(self.request.evaluation_script)

        if self.request.randomized_question:
            with open(
                os.path.join(self.root_directory, "question.txt"),
                "w",
            ) as f:
                f.write(self.request.randomized_question)

        # evaluator.script is the file where the evaluation script has been written to
        language_settings.runtime_options.args = f" evaluator.script {language_settings.filename} {language_settings.runtime_options.args}"
        language_settings.runtime_options.path = (
            self.request.evaluation_script_lang
        )

        return True

    def run_all(self):
        """
        Runs the evaluation script. If successful, we should get the following populated:
        self.reply.evaluation_result_json
        self.reply.overall_status
        self.reply.nsjail_output
        """
        if not self.initialize_environment():
            self.reply.overall_status = (
                request_pb2.CodeReply.ENVIRONMENT_INITIALIZATION_ERROR
            )
            return
        kwargs = {
            "input_path": "",
            "output_path": "",
            "log_path": "",
            "reply_obj": self.reply,
        }
        self.run_program(kwargs)
        self.parse_result()
