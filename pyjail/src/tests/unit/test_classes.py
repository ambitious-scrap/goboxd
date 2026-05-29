"""Classes containing unit tests"""

from abc import ABCMeta, abstractmethod

from code_evaluator import code_runner


class UnitTest(metaclass=ABCMeta):
    """Skeleton class for unit test"""

    @abstractmethod
    def run(self):
        """Function which is called to run the test"""

    @abstractmethod
    def validate(self):
        """Function which validates the results of self.run()"""


class NsJailRunCommandTest(UnitTest):
    """Runs a command using nsjail"""

    def __init__(self, args, expected_out, expected_err):
        """
        Runs a command specified by args and compares output/error with
        expected_out/expected_err.
        """
        self.args = args
        self.expected_out = expected_out
        self.expected_err = expected_err

    def run(self):
        self.out, self.err = code_runner.NsJail.run_command(args=self.args)

    def validate(self):
        return self.out == self.expected_out and self.err == self.expected_err
